# Working in expense-tracker

An event-sourced expense tracker: Go + templ + HTMX on Cloud Run, with Firestore
as an append-only event log. Two binaries — `cmd/server` and `cmd/importer`.

Most of this repo is ordinary Go and needs no special handling. What follows is
the part that is **not** inferable from the code, where the obvious move is the
wrong one. `README.md` explains all of it at length; this is the short version.

## Two Go modules, two different owners

| Module         | Path                             | Who owns it        |
| -------------- | -------------------------------- | ------------------ |
| Root (the app) | `github.com/Zaba505/expense-tracker` | the Go CLI     |
| Dagger module  | `dagger/expense-tracker` (`.dagger/`) | the Dagger CLI |

In the **root** module, normal Go tooling is correct and expected: `go mod tidy`,
`go get`, `go build ./...`, `go test ./...`. Nothing below restricts it.

## `.dagger/` is Dagger-owned — do not use Go tooling on it

**Never run `go mod tidy`, `go get`, or `go mod edit` inside `.dagger/`.** On a
fresh checkout, `go mod tidy` deletes **every `require`** from `.dagger/go.mod` —
exiting 0 and printing nothing, so there is no error to warn you.

Why: every requirement in `.dagger/go.mod` — `genqlient`, `querybuilder`,
`gqlparser`, the OpenTelemetry stack — is imported *only* by generated code
(`.dagger/dagger.gen.go`, `.dagger/internal/dagger/`, `.dagger/internal/telemetry/`),
which is **git-ignored**. On a fresh checkout that code does not exist, so
`go mod tidy` sees every requirement as unused and drops it. The module can then
no longer be generated, and every `dagger call` — i.e. all of CI — fails.

The `replace` directives survive, which is what makes the wreckage easy to miss:
the diff looks like a tidy-up that merely pruned unused dependencies. It is not.
The only correct `go.mod` here is the committed one.

The rules:

- Dependency changes go through **`dagger develop`**, never the Go CLI.
- `.dagger/dagger.gen.go` and `.dagger/internal/` are generated, git-ignored, and
  must not be committed or hand-edited. Do not "fix" a missing import there.
- Only `.dagger/*.go` (hand-written), `.dagger/go.mod`, `.dagger/go.sum`, and
  `dagger.json` are committed.
- If a `.dagger/**` file fails to compile in an editor because
  `dagger/expense-tracker/internal/dagger` is missing, that is expected — run
  `dagger develop` to generate it. It is not a broken import to repair.
- `dagger.json` pins the engine version and the shared `z5labs` dependency. Its
  `engineVersion` must stay in step with `env.DAGGER_VERSION` in
  `.github/workflows/ci.yml`; a CI job fails the run if they drift.

After editing the module: `dagger develop && dagger functions`.

## Build entrypoints — there is no Makefile and no Dockerfile

By design. Every build, check, image, and deploy step is a `dagger call` against
this repo's module, so CI and a laptop run the identical command. The container
image is built by the shared **z5labs `GoApp`** archetype (a `scratch` image, no
hand-written Dockerfile to drift). Do not add a `Makefile`, a `Dockerfile`, or a
CI step that runs `go build`/`go test` directly — CI legs are `dagger call`s.

```sh
dagger call check                                  # gofmt, go vet, golangci-lint, go test -race
dagger call templ-check                            # committed *_templ.go matches the .templ
dagger call ci                                     # full pipeline, both binaries (publishes only with --registry)
dagger call integration-test                       # emulator-backed tests (build tag `integration`)
dagger call image-smoke-test                       # the scratch image actually runs and serves
dagger call terraform validate                     # the Terraform gate; needs no cloud credentials

dagger call server-binary -o ./bin/server          # the exact CI artifact, locally
dagger call importer-binary -o ./bin/importer
dagger call server-image                           # the scratch image CI would publish
dagger call run-against local up --ports 8080:8080 # the app + a seeded emulator — this repo's compose file
dagger call emulator --seed up --ports 8085:8085   # just the emulator, for a `go run ./cmd/server` loop
```

`check` is the fast gate. `go test ./...` in the root module is fine for a quick
inner loop, but it skips the `integration`-tagged tests, which need an emulator —
those only run under `dagger call integration-test`.

## Generated code: templ is committed, Dagger's is not

- **`*_templ.go` IS committed.** The build pipeline deliberately does not run
  templ, so a fresh checkout has to already compile. After editing any `.templ`,
  run **`go tool templ generate`** (templ is pinned by the `tool` directive in
  `go.mod` — use `go tool`, not a `templ` on your `PATH`, or the version-stamped
  output will differ) and commit the result. `dagger call templ-check` fails CI if
  it is stale.
- **Dagger's generated code is NOT committed** — see above.

## Terraform runs only through Dagger

The `deploy/` root module is never run with a `terraform` binary from your `PATH`;
it runs in a pinned container via `dagger call terraform …`, with a short-lived
OAuth token passed explicitly. `validate` is the only subcommand that touches no
cloud, which is why it is the CI gate. `plan`/`apply` are run by hand, and never
from CI. State lives in GCS; `deploy/.terraform.lock.hcl` is committed on purpose,
while `.terraform/`, `*.tfstate*`, and `*.tfvars*` are git-ignored — never commit
one (a stray `*.tfvars` is how a project id and an owner email get published).

## Go conventions

- **Health probes are split.** `/health/liveness` answers for the process only and
  must never touch Firestore — failing it makes Cloud Run restart the container.
  Dependency checks belong behind `/health/readiness`, which sheds traffic instead.
  Do not fold a dependency into liveness.
- **Log with slog's `*Context()` methods** — `logger.ErrorContext(ctx, …)`,
  `logger.InfoContext(ctx, …)` — passing `slog.Attr` values. Not `LogAttrs`. The
  suffix-less `logger.Error`/`logger.Info` are only for call sites with no context,
  like `main`.
- **Money is integer cents** (`internal/money`). Never use a float for an amount.
- **The event log is append-only** (`internal/eventlog`). Events are never mutated
  or deleted; state is derived by replaying them.
- **Emulator-backed tests carry `//go:build integration`**, so the standard
  `go test -race` stage skips them.
- The `scratch` image has no CA bundle and no filesystem, so both `main`s
  blank-import `x509roots/fallback` and `static/` is embedded with `go:embed`.
  Those imports read as unused but are load-bearing — `TestCARootsAreLinkedIn`
  fails if one goes missing. Do not remove them.

## Commits, branches, and PRs

Conventional Commits (`type(scope): subject`) for both commit messages and PR
titles. Work that closes a story issue uses the `story` type and the issue number
as the scope, matching the history:

```
story(issue-9): event type and append-only store
```

Other types in use: `feat`, `chore`, `docs`. Keep the subject imperative and
lowercase.

## Before you open a PR

`dagger call check` and, if you touched a `.templ`, `go tool templ generate`. If
you touched `.dagger/`, confirm `.dagger/go.mod` still lists its requirements —
an emptied `require` block means a Go command was run in there.
