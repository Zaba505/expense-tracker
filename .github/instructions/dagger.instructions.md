---
applyTo: ".dagger/**"
description: "Rules for the Dagger-owned Go module in .dagger/"
---

# You are editing the Dagger module

`.dagger/` is a **separate Go module (`dagger/expense-tracker`) owned by the Dagger
CLI**, not by the Go CLI. It is not part of the root `github.com/Zaba505/expense-tracker`
module, and normal Go dependency tooling destroys it.

## Never run Go dependency commands here

**Do not run `go mod tidy`, `go get`, or `go mod edit` in `.dagger/`.**

Every requirement in `.dagger/go.mod` — `genqlient`, `querybuilder`, `gqlparser`,
the OpenTelemetry stack — is imported *only* by generated code: `.dagger/dagger.gen.go`,
`.dagger/internal/dagger/`, `.dagger/internal/telemetry/`. That code is **git-ignored**,
so on a fresh checkout it does not exist. `go mod tidy` therefore sees every
requirement as unused and deletes it, stripping the `require` blocks down to
nothing — while **exiting 0 and printing no error at all**. The module can then no
longer be generated, and every `dagger call` (all of CI) fails.

The `replace` directives are left behind, so the diff reads like a harmless prune
of unused dependencies. It is not. If you see `.dagger/go.mod` lose its `require`
lines, a Go command was run in here; restore the committed file.

Dependency changes go through **`dagger develop`**.

## Generated code is not yours to write

`.dagger/dagger.gen.go` and `.dagger/internal/` are generated and git-ignored.
**Never commit or hand-edit them.** If an import of
`dagger/expense-tracker/internal/dagger` appears unresolved, the fix is
`dagger develop` — not a new import, a vendored file, or a `go.mod` edit.

Committed here: the hand-written `.dagger/*.go`, `.dagger/go.mod`,
`.dagger/go.sum`, and `dagger.json` at the repo root.

## How to work

```sh
dagger develop     # regenerate the client after editing this module
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
  `engineVersion`, update `env.DAGGER_VERSION` in `.github/workflows/ci.yml` to
  match — a CI job fails the run on drift.
