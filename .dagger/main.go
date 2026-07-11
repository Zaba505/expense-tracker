// A thin Dagger module that runs every expense-tracker Go binary through
// the shared z5labs GoApp archetype, so CI and local builds run the same
// opinionated pipeline (fmt + vet + lint + test -race, scratch image,
// standardized image tags) and every binary shares one build recipe.
//
// The recipe lives entirely in the pinned z5labs dependency (see
// dagger.json); the functions here differ only by package path and binary
// name. Common commands:
//
//	dagger call check                                   # fmt + vet + lint + test -race (no build)
//	dagger call server-binary -o ./bin/server           # the exact CI artifact, single-arch, locally
//	dagger call importer-binary -o ./bin/importer
//	dagger call server-image                            # the scratch image CI would publish
//	dagger call ci                                       # full pipeline for both binaries (publishes on match)
//
// Dagger does not allow a module function to return a dependency's type,
// so each function is terminal: it constructs the z5labs GoApp internally
// and returns a File, Container, or error.
package main

import (
	"context"

	"dagger/expense-tracker/internal/dagger"
)

// cmdServer and cmdImporter are the two binaries' package paths and names.
// Everything else about how they build is identical and owned by z5labs.
const (
	pkgServer   = "./cmd/server"
	pkgImporter = "./cmd/importer"

	binServer   = "server"
	binImporter = "importer"
)

// ExpenseTracker is this repository's Dagger module.
type ExpenseTracker struct{}

// Check runs the shared z5labs check stages once for the whole module:
// gofmt, go vet, golangci-lint, and `go test -race`. It builds nothing,
// so it is the fast pre-commit / PR gate.
func (m *ExpenseTracker) Check(
	ctx context.Context,
	// +defaultPath="/"
	source *dagger.Directory,
) error {
	return dag.Z5Labs().GoLib(source).Ci(ctx)
}

// ServerBinary compiles cmd/server into the exact binary CI publishes
// (CGO disabled, -trimpath, -s -w), single-arch for the host. Export it
// with `-o`:
//
//	dagger call server-binary -o ./bin/server
func (m *ExpenseTracker) ServerBinary(
	// +defaultPath="/"
	source *dagger.Directory,
) *dagger.File {
	return goApp(source, pkgServer, binServer, "", nil).Builder().Binary()
}

// ImporterBinary compiles cmd/importer into the same shape of artifact as
// ServerBinary, single-arch for the host.
func (m *ExpenseTracker) ImporterBinary(
	// +defaultPath="/"
	source *dagger.Directory,
) *dagger.File {
	return goApp(source, pkgImporter, binImporter, "", nil).Builder().Binary()
}

// ServerImage builds the scratch image CI would publish for cmd/server,
// single-arch for the host. Run it, export it, or inspect it:
//
//	dagger call server-image export --path server.tar
func (m *ExpenseTracker) ServerImage(
	// +defaultPath="/"
	source *dagger.Directory,
) *dagger.Container {
	return goApp(source, pkgServer, binServer, "", nil).Builder().Container()
}

// ImporterImage builds the scratch image CI would publish for
// cmd/importer, single-arch for the host.
func (m *ExpenseTracker) ImporterImage(
	// +defaultPath="/"
	source *dagger.Directory,
) *dagger.Container {
	return goApp(source, pkgImporter, binImporter, "", nil).Builder().Container()
}

// Ci runs the full standardized pipeline for BOTH binaries: the shared
// checks, a multi-arch scratch build, and — when registry is set and the
// source's HEAD ref matches z5labs' publishOn filter (default main) — a
// publish. With no registry it is a checks + build gate safe to run
// anywhere.
func (m *ExpenseTracker) Ci(
	ctx context.Context,
	// +defaultPath="/"
	source *dagger.Directory,
	// Container registry to publish to (e.g.
	// "us-docker.pkg.dev/<project>/<repo>"). Empty disables publishing.
	//
	// +optional
	registry string,
	// Registry password/token; required by z5labs when registry is set.
	//
	// +optional
	auth *dagger.Secret,
) error {
	if err := goApp(source, pkgServer, binServer, registry, auth).Ci(ctx); err != nil {
		return err
	}
	return goApp(source, pkgImporter, binImporter, registry, auth).Ci(ctx)
}

// goApp constructs a z5labs GoApp for one binary. Centralizing it keeps
// every binary on one recipe: only pkg and binaryName differ.
func goApp(source *dagger.Directory, pkg, binaryName, registry string, auth *dagger.Secret) *dagger.Z5LabsGoApp {
	return dag.Z5Labs().GoApp(source, dagger.Z5LabsGoAppOpts{
		Pkg:        pkg,
		BinaryName: binaryName,
		Registry:   registry,
		Auth:       auth,
	})
}
