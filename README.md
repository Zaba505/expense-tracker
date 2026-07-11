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
  eventlog/      append-only EventStore (Firestore + in-memory)
  projection/    Fold(events) -> state, rollups, known-types, yearly grid
  web/           net/http ServeMux, handlers, auth middleware, the server
  view/          templ components + the assets' URL space
static/          vendored htmx + CSS (embedded via go:embed)
embed.go         the go:embed of static/ (only the root package can reach it)
deploy/          Terraform (Firestore, Cloud Run, Artifact Registry, ...)
.dagger/         this repo's Dagger module (thin wrapper over z5labs)
dagger.json      pins the shared z5labs build module
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

## Front end

HTML is rendered on the server with [templ](https://templ.guide) and made
interactive with [htmx](https://htmx.org) — no SPA, no bundler, no npm.
Both front-end assets are **vendored into `static/` and embedded into the
binary** (`go:embed`, see `embed.go`), never pulled from a CDN: a deployed
container is self-contained.

| Route          | Serves                                                     |
| -------------- | ---------------------------------------------------------- |
| `GET /`        | the home page (a placeholder until the entry stories land)  |
| `GET /healthz` | `200 ok`                                                    |
| `GET /static/` | the embedded htmx + CSS                                     |

`/healthz` is a **liveness** probe: it reports that the process is up and
serving, and deliberately does not reach out to Firestore, so a slow or
failing dependency cannot get an otherwise-healthy container killed and
restarted into the same failure.

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

### Command reference (replaces the old make targets)

| Goal (old make target)      | Dagger command                                          |
| --------------------------- | ------------------------------------------------------- |
| run checks + tests (`test`) | `dagger call check`                                     |
| build the server binary     | `dagger call server-binary -o ./bin/server`             |
| build the importer binary   | `dagger call importer-binary -o ./bin/importer`         |
| build the server image      | `dagger call server-image export --path ./server.tar`   |
| build the importer image    | `dagger call importer-image export --path ./importer.tar` |
| full CI pipeline (both)     | `dagger call ci`                                        |
| publish (CI, on `main`)     | `dagger call ci --registry=<host> --auth=env:REG_TOKEN` |

- **`check`** runs the shared z5labs stages once — `gofmt`, `go vet`,
  `golangci-lint`, `go test -race` — and builds nothing. Fast PR gate.
- **`server-binary` / `importer-binary`** return the compiled binary as a
  file; `-o <path>` writes it locally. This is byte-for-byte the artifact
  CI publishes (single-arch for your host), via `GoApp.Builder().Binary`.
- **`server-image` / `importer-image`** return the scratch container CI
  would publish (`GoApp.Builder().Container`); chain `export`, `as-tarball`,
  or `publish` as needed.
- **`ci`** runs the full pipeline for both binaries: shared checks, a
  multi-arch scratch build, and — when `--registry` is set and HEAD's ref
  matches z5labs' `publishOn` filter (default `^refs/heads/main$`) — a
  publish. With no `--registry` it's a checks + build gate, safe anywhere.

> The z5labs pipeline deliberately does **not** run `templ generate` or a
> Firestore emulator. CI keeps a `templ`-diff pre-step, and emulator-backed
> tests are build-tagged (`//go:build integration`) and run separately.

### Run locally

The binary loads config from the environment. Against the Firestore
emulator:

```sh
export GCP_PROJECT=demo-local
export OWNER_EMAIL=you@example.com
export FIRESTORE_EMULATOR_HOST=localhost:8181

dagger call server-binary -o ./bin/server
./bin/server
```

Or, for a fast edit loop, plain Go works too (same module, same config):

```sh
go run ./cmd/server
```

### Regenerate templ

HTML is authored in `.templ` files and compiled to Go. After editing a
template, regenerate before building:

```sh
go tool templ generate
```

Unlike the Dagger codegen below, the generated `*_templ.go` files **are
committed**: the build pipeline deliberately does not run `templ generate`,
so a fresh checkout has to already compile. CI re-runs the command and
fails if the result differs from what was committed — if that trips, you
edited a `.templ` without regenerating.

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
