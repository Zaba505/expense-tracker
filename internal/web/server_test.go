package web

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"
)

// testTimeout bounds every wait in this file: a hang is a failure with a
// useful message, not a stuck test binary.
const testTimeout = 10 * time.Second

// listen returns a listener on a free loopback port, plus its address.
func listen(t *testing.T) (net.Listener, string) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return lis, lis.Addr().String()
}

// serve runs Serve in the background and returns a channel carrying its
// return value.
func serve(ctx context.Context, lis net.Listener, h http.Handler) <-chan error {
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, lis, h, slog.New(slog.DiscardHandler)) }()
	return done
}

// awaitServeReturn waits for Serve to return and reports its error.
func awaitServeReturn(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(testTimeout):
		t.Fatal("Serve did not return within the timeout — shutdown is stuck")
		return nil
	}
}

// awaitRefused blocks until addr stops accepting connections, which is how
// a caller can observe from the outside that shutdown has begun.
func awaitRefused(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(testTimeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			return
		}
		conn.Close()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s still accepting connections after shutdown began", addr)
}

func TestServe_ReturnsNilOnCleanShutdown(t *testing.T) {
	t.Parallel()

	lis, addr := listen(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := serve(ctx, lis, http.NotFoundHandler())

	// The server is up and reachable before we ask it to stop.
	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatalf("request to running server: %v", err)
	}
	resp.Body.Close()

	cancel() // the SIGTERM equivalent

	if err := awaitServeReturn(t, done); err != nil {
		t.Errorf("Serve() = %v, want nil on a clean shutdown", err)
	}

	// Serve owns the listener and must close it, or a restarting container
	// would find its own port still bound.
	awaitRefused(t, addr)
}

// TestServe_DrainsInFlightRequest is the graceful part of graceful
// shutdown: a request already being handled when shutdown starts runs to
// completion and gets its real response, rather than a severed connection.
func TestServe_DrainsInFlightRequest(t *testing.T) {
	t.Parallel()

	entered := make(chan struct{}) // handler has begun
	release := make(chan struct{}) // ...and may now finish
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(http.StatusTeapot)
	})

	lis, addr := listen(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := serve(ctx, lis, handler)

	type result struct {
		status int
		err    error
	}
	inFlight := make(chan result, 1)
	go func() {
		resp, err := http.Get("http://" + addr + "/slow")
		if err != nil {
			inFlight <- result{err: err}
			return
		}
		defer resp.Body.Close()
		inFlight <- result{status: resp.StatusCode}
	}()

	<-entered
	cancel()

	// Wait until the listener is closed. Now we know shutdown is genuinely
	// under way while the handler is still inside its request — without
	// this, the request might simply finish before shutdown ever started
	// and the test would prove nothing.
	awaitRefused(t, addr)
	close(release)

	select {
	case got := <-inFlight:
		if got.err != nil {
			t.Fatalf("in-flight request was cut off by shutdown: %v", got.err)
		}
		if got.status != http.StatusTeapot {
			t.Errorf("in-flight request status = %d, want %d", got.status, http.StatusTeapot)
		}
	case <-time.After(testTimeout):
		t.Fatal("in-flight request never completed")
	}

	if err := awaitServeReturn(t, done); err != nil {
		t.Errorf("Serve() = %v, want nil after draining", err)
	}
}

// TestServe_ReportsServeFailure covers the other exit path: the server
// stops on its own, without anyone cancelling the context. That is a
// failure and must surface as one, not as a silent nil.
func TestServe_ReportsServeFailure(t *testing.T) {
	t.Parallel()

	lis, _ := listen(t)
	lis.Close() // accept will fail immediately

	err := awaitServeReturn(t, serve(context.Background(), lis, http.NotFoundHandler()))
	if err == nil {
		t.Fatal("Serve() = nil, want an error when the listener cannot accept")
	}
}
