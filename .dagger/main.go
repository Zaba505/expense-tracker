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
// The z5labs pipeline has no Firestore to talk to, so the emulator-backed
// tests are build-tagged out of it and run separately — and the emulator
// they need is also the one the local dev loop runs against:
//
//	dagger call emulator up --ports 8085:8085           # dev loop: an emulator on localhost:8085
//	dagger call integration-test                        # CI: `go test -tags integration` against its own emulator
//
// Dagger does not allow a module function to return a dependency's type,
// so each function is terminal: it constructs the z5labs GoApp internally
// and returns a File, Container, or error.
package main

import (
	"context"
	"strconv"

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

// The Firestore emulator: Google's own, shipped inside the Cloud SDK
// image (the plain image has no emulators, and no JRE to run them).
const (
	emulatorImage = "gcr.io/google.com/cloudsdktool/google-cloud-cli:emulators"

	// DefaultEmulatorPort is the port the emulator listens on, in the
	// container and — when published with `up --ports` — on the host.
	DefaultEmulatorPort = 8085

	// emulatorService is the hostname a bound emulator answers to inside
	// Dagger's network.
	emulatorService = "firestore"

	// goImage runs the emulator-backed tests. It tracks the toolchain in
	// go.mod; the z5labs module owns the image the build stages use.
	goImage = "golang:1.26"
)

// Emulator is a Firestore emulator, for the local dev loop and for the
// integration tests. Publish it to the host and point the app at it:
//
//	dagger call emulator up --ports 8085:8085
//	FIRESTORE_EMULATOR_HOST=localhost:8085 GCP_PROJECT=demo-expense-tracker \
//	  OWNER_EMAIL=you@example.com go run ./cmd/server
//
// Its data lives only as long as the service does: stop it and the log is
// gone, which is what `go run ./cmd/seed` is for.
func (m *ExpenseTracker) Emulator(
	// Port to listen on, inside the container.
	//
	// +optional
	// +default=8085
	port int,
) *dagger.Service {
	return dag.Container().
		From(emulatorImage).
		WithExposedPort(port).
		AsService(dagger.ContainerAsServiceOpts{
			// 0.0.0.0, not localhost: the emulator has to answer from
			// outside its own container to be worth anything.
			Args: []string{
				"gcloud", "emulators", "firestore", "start",
				"--host-port=0.0.0.0:" + strconv.Itoa(port),
			},
		})
}

// IntegrationTest runs the emulator-backed tests — the ones tagged
// `integration`, which the z5labs `go test -race` stage skips because it
// has no database — against an emulator bound into the test container.
// It is self-contained: no host emulator, no host ports.
func (m *ExpenseTracker) IntegrationTest(
	ctx context.Context,
	// +defaultPath="/"
	source *dagger.Directory,
) (string, error) {
	emulator := m.Emulator(DefaultEmulatorPort)
	emulatorHost := emulatorService + ":" + strconv.Itoa(DefaultEmulatorPort)

	return dag.Container().
		From(goImage).
		// Dagger starts the service and waits for the port to accept a
		// connection before the exec runs, so the tests never race the
		// emulator's (slow, JVM) startup.
		WithServiceBinding(emulatorService, emulator).
		WithEnvVariable("FIRESTORE_EMULATOR_HOST", emulatorHost).
		WithMountedCache("/go/pkg/mod", dag.CacheVolume("expense-tracker-go-mod")).
		WithMountedCache("/root/.cache/go-build", dag.CacheVolume("expense-tracker-go-build")).
		WithMountedDirectory("/src", source).
		WithWorkdir("/src").
		WithExec([]string{"go", "test", "-race", "-tags", "integration", "./..."}).
		Stdout(ctx)
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
