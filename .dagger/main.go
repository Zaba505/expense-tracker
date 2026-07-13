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
//	dagger call templ-check                             # generated *_templ.go is in sync with the .templ
//	dagger call server-binary -o ./bin/server           # the exact CI artifact, single-arch, locally
//	dagger call importer-binary -o ./bin/importer
//	dagger call server-image                            # the scratch image CI would publish
//	dagger call image-smoke-test                        # run that image and check it actually serves
//	dagger call ci                                       # full pipeline for both binaries (publishes on match)
//
// It also owns the app's run configuration and the dependencies that go
// with it — this repo's replacement for a compose file:
//
//	dagger call run-against local up --ports 8080:8080  # the app + a seeded emulator, on localhost:8080
//	dagger call emulator --seed up --ports 8085:8085    # just the emulator, for a `go run ./cmd/server` loop
//	dagger call integration-test                        # `go test -tags integration` against its own emulator
//
// ...and, on the same principle, the cloud the app runs on: the Terraform in
// deploy/ is run from a pinned container with an explicit short-lived token,
// never from a `terraform` on someone's PATH (see terraform.go):
//
//	dagger call terraform validate                       # the CI gate; no cloud, no credentials
//	dagger call terraform --project=… … plan             # and state-bucket / apply / output
//
// The z5labs pipeline is deliberately narrow: it has no Firestore to talk
// to, and it does not run templ. So the emulator-backed tests are
// build-tagged out of it, and both they and the templ-diff check are their
// own functions — which is also how CI runs them, one `dagger call` each.
//
// Dagger does not allow a module function to return a dependency's type,
// so each function is terminal: it constructs the z5labs GoApp internally
// and returns a File, Container, or error.
package main

import (
	"context"
	"fmt"
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

// TemplCheck re-runs `templ generate` and fails if the result differs
// from what is committed. The z5labs pipeline does not run templ, and the
// generated *_templ.go is committed precisely so it doesn't have to — a
// fresh checkout has to already compile. That trade only holds if
// something enforces the two staying in sync; this is that something.
//
// It compares content, not timestamps: templ leaves a file alone when the
// output is unchanged, but a check that keyed on writes rather than bytes
// would rot the moment that stopped being true.
func (m *ExpenseTracker) TemplCheck(
	ctx context.Context,
	// +defaultPath="/"
	source *dagger.Directory,
) error {
	_, err := dag.Container().
		From(goImage).
		WithMountedCache("/go/pkg/mod", dag.CacheVolume("expense-tracker-go-mod")).
		WithMountedCache("/root/.cache/go-build", dag.CacheVolume("expense-tracker-go-build")).
		WithDirectory("/src", source).
		WithWorkdir("/src").
		// templ is pinned by go.mod's tool directive, so the generator
		// here is the one a developer gets from `go tool templ generate` —
		// otherwise CI and the desk would disagree on version-stamped
		// output and this check would fail for the wrong reason.
		WithExec([]string{"sh", "-c", templDiff}).
		Sync(ctx)
	return err
}

// templDiff regenerates in place against a pristine copy and diffs the
// two. `diff` prints exactly which files drifted, and Dagger surfaces a
// failed exec's output, so the error names the file to regenerate.
const templDiff = `set -eu
cp -a /src /before
go tool templ generate
if ! diff -ru /before /src; then
	echo "templ output is stale: run 'go tool templ generate' and commit the *_templ.go files" >&2
	exit 1
fi
`

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

	// eventsCollection is the Firestore collection holding the event log.
	// It mirrors eventlog.EventsCollection, which this module cannot
	// import: .dagger is a separate module, and the app's is internal.
	eventsCollection = "events"

	// goImage runs this module's own Go stages — the templ check and the
	// emulator-backed tests. It tracks the toolchain in go.mod; the z5labs
	// module owns the image the build stages use.
	goImage = "golang:1.26"
)

// firestoreEmulator is the one definition of the dependency. Every caller
// goes through it, because a service is identified by the digest of how
// it was built: construct it two slightly different ways and you get two
// emulators, and whichever one holds the seed data is not the one the app
// is talking to.
//
// It deliberately sets no hostname. A service with a custom hostname
// registers under the DNS domain of whichever module started it, rather
// than the session's, and then nobody else can resolve it — the bug
// behind z5labs/devex#147. Without one, module code, bound containers,
// and the host tunnel all reach it alike.
func firestoreEmulator(port int) *dagger.Service {
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

// Emulator is a Firestore emulator on its own, for the fast edit loop —
// publish it to the host and run the app from source against it:
//
//	dagger call emulator --seed up --ports 8085:8085
//	FIRESTORE_EMULATOR_HOST=localhost:8085 GCP_PROJECT=demo-expense-tracker \
//	  OWNER_EMAIL=you@example.com go run ./cmd/server
//
// For the whole app, dependencies and all, use `run-against local`.
//
// Its data lives only as long as the service does: stop it and the log is
// gone. --seed fills a fresh one with a couple of months of sample
// events, so there is something to look at.
//
// +cache="never"
func (m *ExpenseTracker) Emulator(
	ctx context.Context,
	// Port to listen on, inside the container.
	//
	// +optional
	// +default=8085
	port int,
	// Write a sample event log into it once it is up.
	//
	// +optional
	seed bool,
	// Project id the sample events are written under; must match the
	// GCP_PROJECT the app runs with, since it namespaces the data.
	//
	// +optional
	// +default="demo-expense-tracker"
	project string,
) (*dagger.Service, error) {
	emulator := firestoreEmulator(port)
	if !seed {
		return emulator, nil
	}
	return startAndSeed(ctx, emulator, project)
}

// startAndSeed brings an emulator up and writes the sample log into it,
// returning the *running* service.
//
// Starting it explicitly is what makes the data stick around. A service
// that is only ever bound to a container is released when that container
// is done with it, and dies shortly after; an explicitly started one is
// held for the rest of the session. Since a service is identified by its
// digest, binding this same handle later reaches this same running
// emulator — the one with the events in it — rather than booting a fresh,
// empty one.
func startAndSeed(ctx context.Context, emulator *dagger.Service, project string) (*dagger.Service, error) {
	started, err := emulator.Start(ctx)
	if err != nil {
		return nil, fmt.Errorf("start firestore emulator: %w", err)
	}
	if err := seed(ctx, started, project); err != nil {
		return nil, err
	}
	return started, nil
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
	// Unseeded: the tests write the documents they expect to read, and
	// sample data they did not put there would only be noise.
	emulator := firestoreEmulator(DefaultEmulatorPort)

	return dag.Container().
		From(goImage).
		// Dagger starts the service and waits for the port to accept a
		// connection before the exec runs, so the tests never race the
		// emulator's (slow, JVM) startup.
		WithServiceBinding(emulatorService, emulator).
		WithEnvVariable("FIRESTORE_EMULATOR_HOST", emulatorHost(DefaultEmulatorPort)).
		WithMountedCache("/go/pkg/mod", dag.CacheVolume("expense-tracker-go-mod")).
		WithMountedCache("/root/.cache/go-build", dag.CacheVolume("expense-tracker-go-build")).
		WithMountedDirectory("/src", source).
		WithWorkdir("/src").
		WithExec([]string{"go", "test", "-race", "-tags", "integration", "./..."}).
		Stdout(ctx)
}

// RunAgainst is the app's run configuration, as code. It answers "where
// does this thing run" the way an IDE's run configuration would, except
// it is reproducible and shared: Local() stands the whole stack up on the
// local engine, which is what this repo has instead of a compose file. A
// NonProd() sibling — the same app container, pointed at services that
// already exist somewhere — is the natural next one.
type RunAgainst struct {
	// Source is the repository: the app that gets built and run.
	Source *dagger.Directory
}

// RunAgainst starts the run-configuration chain. Source is contextual, so
// `dagger call run-against local` needs no arguments.
func (m *ExpenseTracker) RunAgainst(
	// +defaultPath="/"
	source *dagger.Directory,
) *RunAgainst {
	return &RunAgainst{Source: source}
}

// Local runs the app against a complete local stack: a Firestore emulator
// with a sample event log already in it, and the server — the very
// container CI builds and publishes, not `go run` — wired to it. One
// command brings up every dependency and the app:
//
//	dagger call run-against local up --ports 8080:8080
//
// Then the app is on localhost:8080, backed by the emulator. Nothing is
// published to the host but the app itself; the emulator is reachable
// only from inside, which is where the app is.
//
// Not cached, and it must not be: a cached call would hand back the
// previous session's service without running any of this — without
// starting an emulator, without seeding it — and the app would come up
// against a database that does not exist.
//
// +cache="never"
func (r *RunAgainst) Local(
	ctx context.Context,
	// Port the app listens on, in the container and on the host.
	//
	// +optional
	// +default=8080
	port int,
	// Google Cloud project id. Against the emulator it only namespaces
	// the data, so any value will do — but the app and the seed have to
	// agree on it.
	//
	// +optional
	// +default="demo-expense-tracker"
	project string,
	// The owner allowlist's single address. Nothing enforces it until the
	// auth stories land.
	//
	// +optional
	// +default="owner@example.com"
	ownerEmail string,
	// Write a sample event log into the emulator before the app starts.
	// Off means an empty database — the app runs, there is nothing in it.
	//
	// +optional
	// +default=true
	seed bool,
) (*dagger.Service, error) {
	emulator := firestoreEmulator(DefaultEmulatorPort)

	// Seeding starts the emulator and holds it up, so the app binds the
	// emulator that has the events in it. Without the seed, nothing has
	// started it yet — binding it below is what will.
	if seed {
		started, err := startAndSeed(ctx, emulator, project)
		if err != nil {
			return nil, err
		}
		emulator = started
	}

	app := goApp(r.Source, pkgServer, binServer, "", nil).Builder().Container().
		WithServiceBinding(emulatorService, emulator).
		WithEnvVariable("FIRESTORE_EMULATOR_HOST", emulatorHost(DefaultEmulatorPort)).
		WithEnvVariable("GCP_PROJECT", project).
		WithEnvVariable("OWNER_EMAIL", ownerEmail).
		WithEnvVariable("PORT", strconv.Itoa(port)).
		WithExposedPort(port)

	// UseEntrypoint: the scratch image is the binary and nothing else —
	// no shell, no default command to fall back on.
	return app.AsService(dagger.ContainerAsServiceOpts{UseEntrypoint: true}), nil
}

// emulatorHost is the address a bound emulator answers on, from inside
// Dagger's network — the FIRESTORE_EMULATOR_HOST every container gets.
func emulatorHost(port int) string {
	return emulatorService + ":" + strconv.Itoa(port)
}

const (
	// publishOn is the ref filter the pipeline publishes on, and it is
	// z5labs' own default — stated here anyway, because the deploy leans on
	// it: it is what makes `deploy` unable to roll out an image built from an
	// unreviewed branch. A push is a side effect on a shared registry, and a
	// filter that decides whether one happens should be visible in this
	// repository rather than inherited silently from a dependency.
	publishOn = "^refs/heads/main$"

	// registryUsername is the literal string Artifact Registry expects when
	// the password is an OAuth2 access token — the token is the password, and
	// this is the user it belongs to. z5labs defaults to "ci", which is right
	// for a registry that takes a real username and wrong for this one.
	registryUsername = "oauth2accesstoken"
)

// goApp constructs a z5labs GoApp for one binary. Centralizing it keeps
// every binary on one recipe: only pkg and binaryName differ.
func goApp(source *dagger.Directory, pkg, binaryName, registry string, auth *dagger.Secret) *dagger.Z5LabsGoApp {
	return dag.Z5Labs().GoApp(source, dagger.Z5LabsGoAppOpts{
		Pkg:          pkg,
		BinaryName:   binaryName,
		PublishOn:    publishOn,
		Registry:     registry,
		AuthUsername: registryUsername,
		Auth:         auth,
	})
}
