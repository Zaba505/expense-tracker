package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"dagger/expense-tracker/internal/dagger"
)

// Everything else in this module tests the source. `check` and
// `integration-test` compile the app and run it as a test binary inside a
// golang container — which has a shell, a libc, a filesystem, and a bundle
// of CA certificates. What we deploy has none of that: one static binary on
// an empty image. Nothing was checking that the app survives the trip, and
// the failures that live in that gap all look the same from here — green
// tests, then a container that will not serve.
//
// So ImageSmokeTest runs the image itself and asks it questions only the
// running container can answer. Each check below exists because a specific,
// plausible mistake would make it fail, and nothing else in the pipeline
// would notice.

const (
	// smokePort is what the container is told to listen on. Deliberately
	// not 8080: Cloud Run injects PORT and expects the app to obey it, so a
	// smoke test on the default port would pass an app that ignored PORT
	// entirely — and that app answers nothing in production.
	smokePort = 9090

	// The app requires both of these to boot. Against an emulator the
	// project id only namespaces the data, and nothing enforces the owner
	// until the auth stories land.
	smokeProject    = "smoke-expense-tracker"
	smokeOwnerEmail = "owner@example.com"

	// smokeBoot bounds the wait for the container to answer at all. It is a
	// static Go binary, so it starts in milliseconds — this is slack for the
	// image build and the emulator behind it, not for the app.
	smokeBoot = 90 * time.Second
)

// smokeCheck is one request the running image has to answer, and the reason
// it is worth making. Body is a substring the response must contain; empty
// means any non-empty body will do.
type smokeCheck struct {
	path   string
	status int
	body   string

	// proves is what a failure of this check would mean. It is not
	// decoration: it is the difference between "the smoke test is red" and
	// knowing which property of the image broke.
	proves string
}

var smokeChecks = []smokeCheck{
	{
		path: "/health/liveness", status: http.StatusOK, body: "ok",
		// The probe touches nothing, so this is a statement about the image
		// and not about the app: the entrypoint at /app/server exists and is
		// executable, the binary is static (a dynamically-linked one dies
		// instantly here — there is no loader and no libc), and it is
		// listening on $PORT rather than on a port of its own choosing.
		proves: "the binary runs on an empty filesystem and honors $PORT",
	},
	{
		path: "/health/readiness", status: http.StatusOK,
		// Readiness round-trips a document through Firestore, so a 200 from
		// inside the scratch image means the whole client stack — gRPC, DNS,
		// the emulator wiring — works there and not just in a golang
		// container. It is 503 with a reason in the log if it cannot.
		proves: "the container can reach its database",
	},
	{
		path: "/static/app.css", status: http.StatusOK,
		// There is no static/ directory in the image; there is no directory
		// at all. This 200 can only have come from the go:embed inside the
		// binary. An app that had drifted back to reading assets off disk
		// would 404 here and nowhere else.
		proves: "the front-end assets are embedded, not read from disk",
	},
	{
		path: "/", status: http.StatusOK, body: "<!doctype html>",
		proves: "the templ-rendered home page is served",
	},
}

// ImageSmokeTest starts the scratch image that `server-image` builds — the
// exact artifact CI publishes — with a Firestore emulator behind it, and
// checks that the running container serves liveness, readiness, the
// embedded assets, and the home page on $PORT.
//
// It is the only thing that tests the deployable rather than the source,
// which is why CI runs it as its own leg.
func (m *ExpenseTracker) ImageSmokeTest(
	ctx context.Context,
	// +defaultPath="/"
	source *dagger.Directory,
) (string, error) {
	// Unseeded: readiness writes the document it reads back, so sample
	// events would prove nothing here and only slow the run down.
	emulator := firestoreEmulator(DefaultEmulatorPort)

	// The image is taken from goApp, not rebuilt here — a smoke test of a
	// container assembled some other way would be a smoke test of nothing.
	//
	// UseEntrypoint: the image is the binary and nothing else, so there is
	// no default command to fall back on.
	app := goApp(source, pkgServer, binServer, "", nil).Builder().Container().
		WithServiceBinding(emulatorService, emulator).
		WithEnvVariable("FIRESTORE_EMULATOR_HOST", emulatorHost(DefaultEmulatorPort)).
		WithEnvVariable("GCP_PROJECT", smokeProject).
		WithEnvVariable("OWNER_EMAIL", smokeOwnerEmail).
		WithEnvVariable("PORT", strconv.Itoa(smokePort)).
		WithExposedPort(smokePort).
		AsService(dagger.ContainerAsServiceOpts{UseEntrypoint: true})

	// Starting it is already a check: a container whose entrypoint exits —
	// a missing binary, a config the app refuses to boot with — fails here,
	// and Dagger surfaces the container's own output with the error.
	started, err := app.Start(ctx)
	if err != nil {
		return "", fmt.Errorf("the server image did not start: %w", err)
	}

	// Reached straight from module code, no curl container in between: a
	// service with no custom hostname registers in the session's DNS domain,
	// which this module can resolve (the same trick seed.go uses).
	endpoint, err := started.Endpoint(ctx, dagger.ServiceEndpointOpts{
		Scheme: "http",
		Port:   smokePort,
	})
	if err != nil {
		return "", fmt.Errorf("server endpoint: %w", err)
	}

	// The app's own timeouts are the ones under test; this only has to be
	// longer than all of them, so a slow answer reads as slow and not as
	// dead. Readiness is the longest, bounded by web.ReadinessTimeout.
	client := &http.Client{Timeout: 30 * time.Second}

	if err := waitForBoot(ctx, client, endpoint); err != nil {
		return "", err
	}

	// From here the checks are strict — no retries. The container has
	// already answered once, so a wrong answer now is a real answer, and
	// retrying it would only turn a bug into a flake.
	var report strings.Builder
	for _, check := range smokeChecks {
		if err := check.run(ctx, client, endpoint); err != nil {
			return "", err
		}
		fmt.Fprintf(&report, "ok   %-20s %s\n", check.path, check.proves)
	}
	return report.String(), nil
}

// waitForBoot polls liveness until the container answers. What it is
// waiting on is mostly the emulator: Dagger holds the app back until the
// service it binds accepts connections, and the app itself is up as soon as
// it is exec'd.
//
// It reports the last failure, not just the timeout, because "the image
// never came up" and "the image came up and 500s" are different bugs.
func waitForBoot(ctx context.Context, client *http.Client, endpoint string) error {
	deadline := time.Now().Add(smokeBoot)
	var last error
	for {
		status, _, err := get(ctx, client, endpoint+"/health/liveness")
		switch {
		case err != nil:
			last = err
		case status != http.StatusOK:
			last = fmt.Errorf("liveness answered %d", status)
		default:
			return nil
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("the server image never came up on port %d within %s: %w",
				smokePort, smokeBoot, last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// run makes the check's request and reports what a mismatch means, so the
// failure names the property that broke rather than a status code.
func (c smokeCheck) run(ctx context.Context, client *http.Client, endpoint string) error {
	status, body, err := get(ctx, client, endpoint+c.path)
	if err != nil {
		return fmt.Errorf("GET %s (%s): %w", c.path, c.proves, err)
	}
	if status != c.status {
		return fmt.Errorf("GET %s: got %d, want %d — no longer true of this image: %s (body: %s)",
			c.path, status, c.status, c.proves, truncate(body))
	}
	if body == "" {
		return fmt.Errorf("GET %s: %d, but the body is empty — no longer true of this image: %s",
			c.path, status, c.proves)
	}
	if c.body != "" && !strings.Contains(body, c.body) {
		return fmt.Errorf("GET %s: body does not contain %q — no longer true of this image: %s (body: %s)",
			c.path, c.body, c.proves, truncate(body))
	}
	return nil
}

// get performs one GET and reads the body, which is bounded: these are
// health probes and a stylesheet, and a response that is not one of those
// should not be able to exhaust this process's memory to say so.
func get(ctx context.Context, client *http.Client, url string) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, "", fmt.Errorf("build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return resp.StatusCode, "", fmt.Errorf("read body: %w", err)
	}
	return resp.StatusCode, string(body), nil
}

// truncate keeps a failure message readable when the body is a whole HTML
// page — the first line of it is what says why.
func truncate(body string) string {
	const max = 200
	body = strings.TrimSpace(body)
	if len(body) > max {
		return body[:max] + "..."
	}
	return body
}
