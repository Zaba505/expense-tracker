# expense-tracker

A single-user, event-sourced expense tracker — Go + HTMX on Google Cloud
Run, backed by Firestore. It replaces a years-old spreadsheet whose
categories were columns (couldn't be retired without deleting history)
and whose rollups were hand-maintained formulas that could drift.

Firestore is an **append-only event log** and the single source of truth;
everything the user sees — a month's values, rollups, the list of known
types, the yearly grid — is a **projection** computed by replaying that
log. Money is stored as `int64` cents throughout.

## Layout

```
cmd/
  server/        HTTP application (Cloud Run)
  importer/      one-off: sheet (converted to parquet) -> events
internal/
  config/        env-driven config, fails fast on missing required values
  money/         integer-cents money type
  domain/        Event and the event-sourcing vocabulary
  eventlog/      Firestore-backed append-only event store
  projection/    Fold(events) -> state, rollups, known-types, yearly grid
  auth/          Google Sign-In (OIDC) and the signed-cookie session
  web/           net/http ServeMux, handlers, auth middleware, the server
  view/          templ components + the assets' URL space
static/          vendored htmx + CSS (embedded via go:embed)
embed.go         the go:embed of static/ (only the root package can reach it)
deploy/          Terraform (Firestore, Cloud Run, Artifact Registry, ...)
.dagger/         this repo's Dagger module (thin wrapper over z5labs)
dagger.json      pins the shared z5labs build module
.github/         CI: one `dagger call` per check, nothing built by hand
```

## Configuration

`internal/config` reads the environment and **fails fast**, reporting
every problem at once:

| Variable                  | Required | Default | Purpose                                            |
| ------------------------- | -------- | ------- | -------------------------------------------------- |
| `GCP_PROJECT`             | yes      | —       | Google Cloud project that owns Firestore           |
| `OWNER_EMAIL`             | yes      | —       | the single account allowed to use the app          |
| `OAUTH_CLIENT_ID`         | yes      | —       | Google Sign-In client (public half)                |
| `OAUTH_CLIENT_SECRET`     | yes      | —       | Google Sign-In client secret (Secret Manager)      |
| `SESSION_KEY`             | yes      | —       | base64 key that signs session cookies (Secret Manager) |
| `PORT`                    | no       | `8080`  | HTTP listen port (Cloud Run injects this)          |
| `FIRESTORE_EMULATOR_HOST` | no       | —       | when set, use a local Firestore emulator           |
| `BASE_URL`                | no       | —       | pins the public origin the OAuth redirect URI is built from |

The three auth variables are **required**, so a deployment that could not
complete a sign-in refuses to start rather than serving a login page that
cannot work. Generate a session key with `openssl rand -base64 32`.

## Sign-in

`internal/auth` signs the owner in with Google over ordinary OIDC:
`GET /auth/login` mints a state and a nonce and redirects to Google;
`GET /auth/callback` checks the state, redeems the code, verifies the ID
token's signature, issuer, audience and nonce, and sets a signed session
cookie; `GET /logout` clears that cookie.

The session **is** the cookie — there is no session store. That fits the
deployment rather than saving a table: the app scales to zero across
interchangeable Cloud Run instances, so a session held in one instance's
memory would be gone on the next cold start and unknown to the other
instance. A cookie signed with `SESSION_KEY` is verifiable by any instance,
including one that has not started yet. The price is that a session cannot
be revoked server-side, which is why it expires in a day and why the key is
a secret: **anyone holding `SESSION_KEY` can mint a cookie claiming to be
the owner.** Rotating it (a new Secret Manager version) invalidates every
session, which is the same thing as revoking them all.

`BASE_URL` is normally unset: the redirect URI is built from the request the
app is serving, which makes the same binary work on `http://localhost:8080`
and on a Cloud Run URL that did not exist when the config was written — the
URL is an *output* of creating the service. Set it only behind a custom
domain whose host is not the one reaching the container.

`internal/web` is the app's authorization gate: one middleware allows the
public auth and health routes through, and rejects every other route unless
the session's email matches `OWNER_EMAIL`. That middleware is also the one
swap point for a future move from signed-cookie sessions to IAP.

## Event log

`internal/eventlog` is the Firestore client: one `Store`, used unchanged in
both environments. It authenticates to the live service with **Application
Default Credentials**, or talks to a **local emulator** when
`FIRESTORE_EMULATOR_HOST` is set — the app never learns which.

Connecting is lazy, so a Firestore that is down does not stop the server
from booting; it makes it report itself unready. That is what the two
probes are for, and why there are two:

| Probe                   | Answers                            | What the platform does with a failure |
| ----------------------- | ---------------------------------- | ------------------------------------- |
| `GET /health/liveness`  | is the process up?                 | restarts the container                |
| `GET /health/readiness` | can it reach the event log?        | holds traffic back, container survives |

Readiness round-trips a document through `meta/health` — it writes, then
reads back what it wrote, because a write alone would not catch a database
that accepts writes but cannot serve reads. Liveness deliberately touches
nothing: restarting the container cannot fix a database that is down, so
folding Firestore into liveness would turn a blip into a restart loop.

## Importing the spreadsheet — `cmd/importer`

The five years of history that lived in a spreadsheet are loaded by
`cmd/importer`, from a **Parquet** file rather than the sheet's own CSV
export. A conversion script — the owner's, ad hoc, run once — unpivots the
sheet into **one row per event** and says what each row is: `month`, `type`,
`amount_cents`, `direction`. A column heading cannot say whether the number
under it is a bill, a paycheck, or a formula, so a wide export would leave
the importer guessing, silently, into a log that cannot be edited afterwards.
The rollup columns simply produce no rows, because a rollup is not an event.

```sh
importer -file history.parquet -dry-run    # what it would do
importer -file history.parquet             # do it
```

The project comes from `-project` or `GCP_PROJECT`; set `-emulator-host` or
`FIRESTORE_EMULATOR_HOST` to point it at an emulator instead of the live
database. The report goes to stdout, the log to stderr.

**Re-running it is safe, and is how an interrupted import is meant to be
finished.** Every row is appended under an idempotency key derived from the
cell it came from — the month, the type, and the direction, which are exactly
the three fields the fold keys a cell by. The key *is* the Firestore document
ID, so the check that a row has not already been imported is Firestore's
`Create` refusing a document that is already there: it is atomic with the
write, and two importers racing cannot both find a row absent and both append
it. It also means there is no state file to keep and no ledger to fall out of
step with the log — **the record of what has been imported is the log**.

The amount is deliberately *not* part of the key. A row whose figure was
edited in the sheet after it was imported is therefore recognized as the cell
it already is, reported as a **divergence**, and left alone:

```
divergences (1)
The log already holds these cells, with a different amount. Nothing was
written for them.

  row 1     2026-01 / Rent (expense)
            log    $1,200.00
            sheet  $1,250.00
```

The importer never argues with the log about the past. Were the amount in the
key, an edited row would mint a fresh key and land as a *second* `set` event
for a cell that already had one — and since a replayed row is dated at the
first instant of the month it belongs to, the two would tie on `recorded_at`
and the fold would pick between them by document ID, which is to say
arbitrarily. Corrections belong in the app, where they are dated when they are
made and so land last, on purpose. A divergence exits non-zero; everything
else in the file is still imported.

The same rule protects entries the **app** made first. "A correction in the app
lands last" holds because an entry made *during* a month is later than the first
instant of it — but the app lets you enter a month before it arrives (next
month's rent is knowable), and such an entry is *older* than the row the import
would date at the month's start. It would be superseded. So the rule is stated
rather than inferred from the calendar: **an imported row is appended only when
it would sort strictly before every event the log already has for that cell.**
When it would not, nothing is written and the row is reported as a **conflict** —
the sheet does not get to overrule what you entered.

If a run dies partway — a laptop that slept, a network that dropped — the report
still prints, saying how many rows landed and that the rollups are therefore
unknown. What was appended is correct and keyed; re-running finishes the job.

Finally, the report prints **what each month folds to** — expenses, income,
net — to be read against the sheet's own rollup columns. It is a table for a
human rather than a diff against a machine-readable copy of those columns,
because there is nothing to diff against: `Monthly Bills Total`, `Income` and
`Savings` were *formulas*, and here they are recomputed from the events on
every read. A side file of expected totals would be a copy of a number the
fold already derives, and a copy is a thing that can be wrong.

## Front end

HTML is rendered on the server with [templ](https://templ.guide) and made
interactive with [htmx](https://htmx.org) — no SPA, no bundler, no npm.
Both front-end assets are **vendored into `static/` and embedded into the
binary** (`go:embed`, see `embed.go`), never pulled from a CDN: a deployed
container is self-contained.

| Route                     | Serves                                                    |
| ------------------------- | --------------------------------------------------------- |
| `GET /`                   | the home page (a placeholder until the entry stories land) |
| `GET /reports/{year}`     | the yearly grid: month rows, type columns, live rollups    |
| `GET /health/liveness`    | `200 ok`                                                   |
| `GET /health/readiness`   | `200` / `503` + JSON, after a Firestore round-trip         |
| `GET /static/`            | the embedded htmx + CSS                                    |

htmx is vendored at **2.0.7**. To move to a new version, replace the file
and update that number here:

```sh
curl -o static/htmx.min.js https://unpkg.com/htmx.org@<version>/dist/htmx.min.js
```

## Build & dev tooling — Dagger, not Make

All build/CI/image/publish tooling standardizes on [Dagger](https://dagger.io)
via the shared **z5labs** daggerverse module
(`github.com/z5labs/devex/daggerverse/z5labs`), pinned in `dagger.json`.
There is **no `Makefile` and no hand-written `Dockerfile`** — every Go
binary is built by the z5labs `GoApp` archetype, so CI and local builds
produce the *identical* artifact: `CGO_ENABLED=0`, `-trimpath`,
`-ldflags "-s -w"`, packaged into a `scratch` image with standardized
SHA/commit-time (branches) or tag-based (tags) image tags.

`.dagger/` is a thin project module: each binary is one function that runs
it through `GoApp`. The two binaries differ only by package path and name;
the whole build recipe lives in the pinned dependency.

Prereqs: the [`dagger` CLI](https://docs.dagger.io/install) and a
container runtime (Docker or Podman). Run everything from the repo root.

### The container image

Each binary ships as a **`scratch` image**: the static binary at
`/app/<name>`, set as the entrypoint, and *nothing else* — no shell, no
package manager, no `/etc`, no filesystem to speak of. It is built for
`linux/amd64` and `linux/arm64`. Nobody in this repo writes that image;
`GoApp` does, the same way for every z5labs binary, which is why there is
no `Dockerfile` to review or to drift.

An empty image is only viable if the binary is genuinely self-contained,
and that is a claim about the **app**, not about the build. Two things
make it true here, and both are easy to undo by accident:

- **The front end is in the binary.** `static/` is embedded with
  `go:embed` (`embed.go`), so there is no directory for the image to be
  missing.
- **The CA roots are in the binary.** A scratch image carries no root
  certificates, so every outbound TLS call — Firestore, Google APIs, OIDC
  later — would fail with *"certificate signed by unknown authority"*. Both
  `main`s blank-import
  [`x509roots/fallback`](https://pkg.go.dev/golang.org/x/crypto/x509roots/fallback),
  which installs Mozilla's bundle as `crypto/x509`'s fallback roots. It is
  a load-bearing import that reads like an unused one, so a test in each
  `cmd` fails if it goes missing (`TestCARootsAreLinkedIn`).

Both claims are only really settled by running the thing, which is what
`dagger call image-smoke-test` does — see below.

### Command reference (replaces the old make targets)

| Goal (old make target)      | Dagger command                                          |
| --------------------------- | ------------------------------------------------------- |
| run checks + tests (`test`) | `dagger call check`                                     |
| generated templ is in sync  | `dagger call templ-check`                               |
| build the server binary     | `dagger call server-binary -o ./bin/server`             |
| build the importer binary   | `dagger call importer-binary -o ./bin/importer`         |
| build the server image      | `dagger call server-image export --path ./server.tar`   |
| build the importer image    | `dagger call importer-image export --path ./importer.tar` |
| check the image really runs | `dagger call image-smoke-test`                          |
| full CI pipeline (both)     | `dagger call ci`                                        |
| publish (CI, on `main`)     | `dagger call ci --registry=<host> --auth=env:REG_TOKEN` |
| the tag this commit gets    | `dagger call image-tag`                                 |
| publish **and** deploy      | `dagger call deploy --project=… --access-token=…`       |
| run the app + its deps      | `dagger call run-against local up --ports 8080:8080`    |
| a Firestore emulator alone  | `dagger call emulator --seed up --ports 8085:8085`      |
| emulator-backed tests       | `dagger call integration-test`                          |
| check the Terraform         | `dagger call terraform validate`                        |
| plan / apply the infra      | `dagger call terraform --project=… … plan` (see `deploy/`) |

- **`check`** runs the shared z5labs stages once — `gofmt`, `go vet`,
  `golangci-lint`, `go test -race` — and builds nothing. Fast PR gate.
- **`templ-check`** re-runs `templ generate` and fails if the result differs
  from what is committed, printing the file that drifted. It compares
  content, not write times.
- **`server-binary` / `importer-binary`** return the compiled binary as a
  file; `-o <path>` writes it locally. This is byte-for-byte the artifact
  CI publishes (single-arch for your host), via `GoApp.Builder().Binary`.
- **`server-image` / `importer-image`** return the scratch container CI
  would publish (`GoApp.Builder().Container`); chain `export`, `as-tarball`,
  or `publish` as needed.
- **`image-smoke-test`** starts the scratch image itself, with a Firestore
  emulator behind it, and asks it for the four routes that prove it is
  deployable: liveness (the static binary runs on an empty filesystem and
  listens on `$PORT` — the test deliberately hands it a port that is *not*
  the default), readiness (the container can reach its database), the
  embedded stylesheet (assets are in the binary, not on a disk that does not
  exist), and the home page. Every other check in this repo tests the
  *source* from inside a golang container — which has a shell, a libc and a
  CA bundle. This is the only one that tests the thing we deploy.
- **`ci`** runs the full pipeline for both binaries: shared checks, a
  multi-arch scratch build, and — when `--registry` is set and HEAD's ref
  matches the `publishOn` filter (`^refs/heads/main$`) — a publish. With no
  `--registry` it's a checks + build gate, safe anywhere.
- **`image-tag`** prints the tag this commit's images are published under —
  `<shortSha>-<commitISO>`, z5labs' scheme for a branch build. `deploy` needs
  it because the pipeline does not report what it pushed.
- **`deploy`** runs `ci` with a registry (so the image is published by the
  shared pipeline, not by a build of its own) and then rolls the Cloud Run
  service onto that exact tag. It is the same command CI runs — see below.
- **`run-against local`** is the run configuration (see below).
- **`emulator`** is a Firestore emulator on its own, as a Dagger service.
  `--seed` fills it with a couple of months of sample events; `up --ports
  8085:8085` publishes it on the host. Its data lives only as long as the
  service does.
- **`integration-test`** runs the emulator-backed tests (`//go:build
  integration`) against an emulator bound into the test container. It needs
  no host emulator and no host ports, so CI can run it as-is.
- **`terraform`** runs the `deploy/` root module — `validate`, `plan`,
  `apply`, `output`, and the one-off `state-bucket` (see below).

> The z5labs pipeline deliberately does **not** run `templ generate` or a
> Firestore emulator. That is why `templ-check` and `integration-test` are
> functions of their own: the emulator-backed tests are build-tagged out of
> `check`, and the templ diff is what keeps the committed `*_templ.go`
> honest. CI runs each as its own leg.

### Continuous integration

`.github/workflows/ci.yml` runs on every PR and on pushes to `main`. It
supplies a runner and a fan-out and nothing else — each leg is one of the
commands above, so whatever CI catches you reproduce with the same
`dagger call`, and there is no second, CI-only definition of the build to
drift from this one:

| CI leg                      | Command                        |
| --------------------------- | ------------------------------ |
| templ generate is committed | `dagger call templ-check`      |
| fmt, vet, lint, test, build | `dagger call ci`               |
| Firestore integration tests | `dagger call integration-test` |
| the image runs and serves   | `dagger call image-smoke-test` |
| Terraform is valid          | `dagger call terraform validate` |

The legs run in parallel and report independently (`fail-fast: false`), so a
lint failure cannot hide a broken integration test — you fix them in one
pass, not two. None of them is passed a `--registry`, so they build both
images and publish nothing: they are checks, and a check should not be able
to change the world.

### Continuous deployment

A push to `main` that passes every leg above then runs one more:

```sh
dagger call deploy --project=<id> --access-token=<token>
```

which publishes this commit's images through the z5labs pipeline and points
Cloud Run at the server one. `needs: [check]` is the gate, and being a job in
the same workflow makes it a tight one: the deploy runs on the very commit the
checks passed on, from the same checkout.

**There is no service-account key** — not in the repository, not in GitHub's
secrets, not on a desk. GitHub mints an OIDC token for the run, Google's
workload identity pool exchanges it for an access token good for the hour, and
that token is the whole of what the deploy holds. The pool only accepts tokens
whose `repository` claim is this repository, which is the trust boundary of the
entire arrangement and is argued in [`deploy/wif.tf`](deploy/wif.tf). One token
does both halves of the job: Artifact Registry takes it as the password for
`oauth2accesstoken`, and `gcloud run deploy` reads it from the environment.

Three things fence the deploy in, and they are independent:

- the job runs only on a push to `main`, only after the checks are green;
- the pipeline publishes only for a HEAD ref matching `^refs/heads/main$`, so
  a job that somehow ran elsewhere would push nothing;
- the token cannot be minted by a workflow in any other repository, so a fork's
  pull request cannot deploy — its OIDC token carries the fork's own claim.

**What proves the deployed app works is the rollout itself.** `gcloud run
deploy` does not return until the new revision passes its startup probe, and
that probe is `GET /health/readiness` — which round-trips a document through
Firestore. So a green deploy is a deployed service that has already written to
and read from the event log as the runtime account. A revision that cannot
reach Firestore never takes traffic, and the previous one keeps serving it.

Terraform owns the service; the deploy owns only the image it runs (see
[`deploy/run.tf`](deploy/run.tf)). If the service does not exist yet, the
deploy refuses rather than creating one without any of that configuration.
It skips itself entirely until the repository variables in
[`deploy/README.md`](deploy/README.md) are set — until an environment exists,
there is nothing to deploy to.

`DAGGER_VERSION` in the workflow must equal `engineVersion` in `dagger.json`
— the run fails fast if they drift, since the CLI provisions the engine of
its own version and a mismatch would run the module against an engine it was
not generated for. Bump both together. The workflow caches the engine image
so a run does not re-pull it.

One sharp edge: `dagger call ci` needs a real `.git` **directory**, because
the z5labs pipeline reads the refs at `HEAD` to decide whether to publish. It
therefore fails from inside a `git worktree`, where `.git` is a file pointing
at the parent repo. The same goes for `image-tag` and `deploy`, which read the
commit to know which tag they are talking about. Run those from a normal
clone; `check`, `templ-check`, and `integration-test` do not care.

### The cloud footprint — `deploy/`, run through Dagger

The infrastructure is a Terraform root module in [`deploy/`](deploy/):
Firestore (native mode, the event log), Artifact Registry, the Cloud Run
service, the service account the app runs as (`roles/datastore.user` and
nothing more), Secret Manager, and the Workload Identity pool that lets
GitHub Actions deploy without a service-account key.

**It runs through Dagger, like everything else here** — there is no
`terraform` on your PATH in this project:

```sh
dagger call terraform validate               # fmt + validate; no cloud, no credentials

export TF_ARGS="--project=<id> --owner-email=<you@example.com> \
  --oauth-client-id=<...apps.googleusercontent.com> \
  --access-token=cmd://\"gcloud auth print-access-token\""

dagger call terraform $TF_ARGS state-bucket  # once per project; idempotent
dagger call terraform $TF_ARGS plan
dagger call terraform $TF_ARGS apply
dagger call terraform $TF_ARGS output        # JSON: registry, service, identities
```

The same argument the build makes: a `terraform` on someone's PATH is a
version, a plugin cache and a set of ambient credentials nobody else has —
and it applies to production. Here the CLI is pinned to an image, the
credentials are an explicit `Secret` argument, and the command CI runs is the
command you run. Credentials are a **short-lived access token**, never a
service-account key, which is the same posture the deploy pipeline takes with
Workload Identity Federation: no static cloud key exists, in the repo or in
GitHub.

`state-bucket` is a separate command because Terraform initializes its
backend before it evaluates any configuration — the GCS bucket its state
lives in cannot be a resource in the module that stores state there. It is
the one bootstrap step, and it is a command rather than a paragraph you
follow by hand.

`deploy/README.md` covers standing up an environment end to end. Three things
worth knowing from here: the service is still created **private** — the app
now signs people in (#13), but nothing yet stops one from being anybody, so
the edge stays shut until the middleware lands (#14); Terraform owns the
Cloud Run service but **not its image** — `gcloud run deploy` does, so an
apply never rolls the running build back; and the service now reads two
secrets out of Secret Manager, so **a revision will not start until both have
a version**. Adding them is a one-time, out-of-band step, and `deploy/README.md`
spells it out.

### Run locally — `run-against local`

The app's **run configuration lives in the Dagger module**, not in a README
you re-enact by hand: `RunAgainst` says what the app needs to run and how
its dependencies are wired, and one command brings all of it up. It is what
this repo has instead of a compose file.

```sh
dagger call run-against local up --ports 8080:8080
```

That stands up a Firestore emulator, writes a sample event log into it, and
runs the server against it — **the same scratch container CI builds and
publishes**, not `go run`. The app lands on `localhost:8080`; the emulator
stays inside, reachable only from the app. `curl localhost:8080/health/readiness`
returning `200` means the container wrote to the emulator and read it back.

The seed lives in the run configuration too (`.dagger/seed.go`), because
sample data is a property of *how you run this locally*, not of the product
— there is no seeder binary to ship or to remember to run. It cannot touch
real Firestore, either: it only ever talks to an emulator the chain started
itself, which matters because an append-only log has no undo.

For a faster edit loop, run the app from source against a published
emulator instead:

```sh
dagger call emulator --seed up --ports 8085:8085     # one shell

export GCP_PROJECT=demo-expense-tracker              # another
export OWNER_EMAIL=you@example.com
export FIRESTORE_EMULATOR_HOST=localhost:8085
export OAUTH_CLIENT_ID=local.apps.googleusercontent.com
export OAUTH_CLIENT_SECRET=x
export SESSION_KEY="$(openssl rand -base64 32)"
go run ./cmd/server
```

`FIRESTORE_EMULATOR_HOST` is the only difference between that and
production. The OAuth values above are placeholders — enough for the app to
boot and serve, which is all a run against a fake database needs. To actually
sign in locally, pass a **real** client and register
`http://localhost:8080/auth/callback` as one of its authorized redirect URIs;
Google will not redirect to a URI it was not told about. The same goes for
`run-against local`, which takes `--oauth-client-id` and
`--oauth-client-secret` for exactly that.

### Regenerate templ

HTML is authored in `.templ` files and compiled to Go. After editing a
template, regenerate before building:

```sh
go tool templ generate
```

The generated `*_templ.go` files **are committed**: the build pipeline
deliberately does not run `templ generate`, so a fresh checkout has to
already compile. CI re-runs the command (`dagger call templ-check`) and
fails if the result differs from what was committed — if that trips, you
edited a `.templ` without regenerating.

### Working on the Dagger module itself

`.dagger/` is a normal Go Dagger module. After editing it, regenerate its
client and list functions:

```sh
dagger develop
dagger functions
```

Commit whatever `dagger develop` writes. The generated client
(`.dagger/dagger.gen.go`, `.dagger/internal/dagger/`) is committed for the
same reason `*_templ.go` is — a fresh checkout has to compile without first
running a code generator — but here there is a second, sharper reason: that
client is the *only* importer of `.dagger/go.mod`'s requirements. Where it
does not exist, `go mod tidy` finds every requirement unused and deletes
them all, silently, and the module can no longer be generated at all. So the
one Go tooling rule in `.dagger/` is: **never run `go mod tidy`, `go get`,
or `go mod edit` in there** — dependency changes come from `dagger develop`,
which writes the requirements and the client that uses them together. Every
other Go tool (`go build`, `go vet`, gopls) works in there normally.

CI regenerates and fails the run if the committed client is stale — from a
module edit that was not regenerated, or an `engineVersion` bump that was
not regenerated against.
