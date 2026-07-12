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
  importer/      one-off: sheet CSV -> events
internal/
  config/        env-driven config, fails fast on missing required values
  money/         integer-cents money type
  domain/        Event and the event-sourcing vocabulary
  eventlog/      Firestore-backed append-only event store
  projection/    Fold(events) -> state, rollups, known-types, yearly grid
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
| `PORT`                    | no       | `8080`  | HTTP listen port (Cloud Run injects this)          |
| `FIRESTORE_EMULATOR_HOST` | no       | —       | when set, use a local Firestore emulator           |

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

## Front end

HTML is rendered on the server with [templ](https://templ.guide) and made
interactive with [htmx](https://htmx.org) — no SPA, no bundler, no npm.
Both front-end assets are **vendored into `static/` and embedded into the
binary** (`go:embed`, see `embed.go`), never pulled from a CDN: a deployed
container is self-contained.

| Route                     | Serves                                                    |
| ------------------------- | --------------------------------------------------------- |
| `GET /`                   | the home page (a placeholder until the entry stories land) |
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
  matches z5labs' `publishOn` filter (default `^refs/heads/main$`) — a
  publish. With no `--registry` it's a checks + build gate, safe anywhere.
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
pass, not two. No `--registry` is passed, so `ci` builds both images and
publishes nothing; publishing is deploy-time and belongs to the deploy
story.

`DAGGER_VERSION` in the workflow must equal `engineVersion` in `dagger.json`
— the run fails fast if they drift, since the CLI provisions the engine of
its own version and a mismatch would run the module against an engine it was
not generated for. Bump both together. The workflow caches the engine image
so a run does not re-pull it.

One sharp edge: `dagger call ci` needs a real `.git` **directory**, because
the z5labs pipeline reads the refs at `HEAD` to decide whether to publish. It
therefore fails from inside a `git worktree`, where `.git` is a file pointing
at the parent repo. Run it from a normal clone; `check`, `templ-check`, and
`integration-test` do not care.

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

`deploy/README.md` covers standing up an environment end to end. The two
things worth knowing from here: the service is created **private** and stays
that way until the app authenticates its own callers (#13), and Terraform
owns the Cloud Run service but **not its image** — `gcloud run deploy` does,
so an apply never rolls the running build back.

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
go run ./cmd/server
```

`FIRESTORE_EMULATOR_HOST` is the only difference between that and
production.

### Regenerate templ

HTML is authored in `.templ` files and compiled to Go. After editing a
template, regenerate before building:

```sh
go tool templ generate
```

Unlike the Dagger codegen below, the generated `*_templ.go` files **are
committed**: the build pipeline deliberately does not run `templ generate`,
so a fresh checkout has to already compile. CI re-runs the command
(`dagger call templ-check`) and fails if the result differs from what was
committed — if that trips, you edited a `.templ` without regenerating.

### Working on the Dagger module itself

`.dagger/` is a normal Go Dagger module. After editing `.dagger/main.go`,
regenerate its client and list functions:

```sh
dagger develop
dagger functions
```

Codegen (`.dagger/internal/dagger`, `.dagger/dagger.gen.go`) is
git-ignored and regenerated on demand — only `.dagger/main.go`,
`.dagger/go.mod`, `.dagger/go.sum`, and `dagger.json` are committed.
