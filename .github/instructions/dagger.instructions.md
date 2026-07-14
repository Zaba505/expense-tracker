---
applyTo: ".dagger/**"
description: "Rules for the Dagger-owned Go module in .dagger/"
---

# You are editing the Dagger module

`.dagger/` is a **separate Go module (`dagger/expense-tracker`) whose dependencies
belong to the Dagger CLI**, not to the Go CLI. It is not part of the root
`github.com/Zaba505/expense-tracker` module, and Go's dependency tooling destroys it.

## Never run Go *dependency* commands here

**Do not run `go mod tidy`, `go get`, or `go mod edit` in `.dagger/`.**
`dagger develop` owns this module's requirements *and* its generated client, and
writes the two together. A Go dependency command edits one half of that pair from
outside, and nothing puts it back.

Every requirement in `.dagger/go.mod` — `genqlient`, `querybuilder`, `gqlparser`,
`otel-go` — is imported *only* by the generated client (`.dagger/dagger.gen.go`,
`.dagger/internal/dagger/`). That client is committed, which is the one thing
making `go mod tidy` a no-op here. Delete it, or tidy mid-regeneration, and tidy
finds every requirement unused and deletes the lot — while **exiting 0 and
printing no error at all**. The `replace` directives are left behind, so the diff
reads like a harmless prune of unused dependencies. It is not: the module can no
longer be generated, and every `dagger call` (all of CI) fails. That is #40.

If you see `.dagger/go.mod` lose its `require` lines, a Go dependency command was
run in here; restore the committed file.

## Generated code is committed, but is not yours to write

`.dagger/dagger.gen.go` and `.dagger/internal/dagger/` are **committed** — that
is what lets `go build`, `go vet`, `go test`, and gopls work in here on a fresh
checkout with no Dagger CLI installed.

**Never hand-edit them.** They change only as output of `dagger develop`, and
that output is committed alongside the module edit that caused it; the `codegen`
job in `.github/workflows/ci.yml` regenerates and fails the run if what is
committed has gone stale. If an import of `dagger/expense-tracker/internal/dagger`
appears unresolved, the fix is `dagger develop` — not a new import, a vendored
file, or a `go.mod` edit.

**Do not tidy `.dagger/.gitignore` or `.dagger/.gitattributes`.** `dagger develop`
re-appends any of its default ignore entries it cannot find in the file, so the
`!` lines in `.gitignore` are what keep the committed client from being ignored
again on the next run — and an ignored client means a *newly* generated file
silently misses the commit. Both files explain themselves; read them before
touching them.

## How to work

```sh
dagger develop     # regenerate the client after editing this module — commit what it writes
dagger functions   # list the module's entrypoints
dagger call check  # the repo's fmt/vet/lint/test gate
```

## Module conventions

- Dagger cannot return a dependency's type from a module function, so **every
  function is terminal**: it constructs the shared z5labs `GoApp` internally and
  returns a `*dagger.File`, `*dagger.Container`, `*dagger.Service`, `string`, or
  `error`. Do not try to return a `*dagger.Z5LabsGoApp`.
- The build recipe lives in the pinned **z5labs** dependency, not here. The
  per-binary functions differ only by package path and binary name (`goApp()` in
  `main.go`) — there is no `Dockerfile` and no `Makefile` in this repo by design.
- The z5labs pipeline runs neither templ nor a Firestore emulator. That is why
  `TemplCheck` and `IntegrationTest` are functions of their own.
- Build one Firestore emulator through `firestoreEmulator()` and nothing else. A
  service is identified by the digest of how it was built, so constructing it two
  slightly different ways yields two emulators — and the one holding the seed data
  will not be the one the app is talking to. Do not give it a custom hostname.
- Functions that stand up a live service (`Emulator`, `RunAgainst.Local`) are
  marked `+cache="never"`; a cached call would hand back a dead service.
- `dagger.json` pins `engineVersion` and the `z5labs` dependency. If you change
  either, re-run `dagger develop` and commit the regenerated client — the new
  engine generates a different one. For `engineVersion`, also update
  `env.DAGGER_VERSION` in `.github/workflows/ci.yml` to match; a CI job fails the
  run on drift.
