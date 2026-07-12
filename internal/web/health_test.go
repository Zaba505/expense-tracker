package web

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// readinessResponse decodes the probe's body so the tests assert on what
// a platform would actually parse, not on a substring of it.
func readinessResponse(t *testing.T, rec *httptest.ResponseRecorder) readiness {
	t.Helper()

	var body readiness
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding readiness body %q: %v", rec.Body.String(), err)
	}
	return body
}

func TestReadiness_Healthy(t *testing.T) {
	t.Parallel()

	store := &stubChecker{}
	rec := httptest.NewRecorder()
	NewHandler(slog.New(slog.DiscardHandler), store).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/readiness", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("GET /health/readiness status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store — a cached probe reports stale health", got)
	}

	body := readinessResponse(t, rec)
	if body.Status != "ok" || body.Firestore != "ok" {
		t.Errorf("body = %+v, want status and firestore both ok", body)
	}

	// The store really was asked, rather than the handler assuming health.
	if store.called != 1 {
		t.Errorf("event store checked %d times, want 1", store.called)
	}
}

// TestReadiness_Unreachable is the behaviour Cloud Run keys off: a 503
// takes the instance out of rotation instead of restarting it.
func TestReadiness_Unreachable(t *testing.T) {
	t.Parallel()

	store := &stubChecker{err: errors.New("dial firestore: connection refused")}
	rec := httptest.NewRecorder()
	NewHandler(slog.New(slog.DiscardHandler), store).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/readiness", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("GET /health/readiness status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}

	body := readinessResponse(t, rec)
	if body.Status != "unavailable" || body.Firestore != "unreachable" {
		t.Errorf("body = %+v, want status unavailable and firestore unreachable", body)
	}

	// The endpoint is unauthenticated: the failure's details belong in the
	// log, not in the response, where they would name the project and the
	// database to anyone who asks.
	if raw := rec.Body.String(); strings.Contains(raw, "connection refused") {
		t.Errorf("readiness body leaks the underlying error:\n%s", raw)
	}
}

// TestReadiness_BoundsTheCheck proves the probe answers on its own
// deadline. A hung Firestore fails by never replying, so a check without
// a deadline would hang with it and the platform would learn nothing.
func TestReadiness_BoundsTheCheck(t *testing.T) {
	t.Parallel()

	deadlines := make(chan time.Time, 1)
	store := checkerFunc(func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Error("readiness check ran without a deadline")
		}
		deadlines <- deadline
		return ctx.Err()
	})

	rec := httptest.NewRecorder()
	NewHandler(slog.New(slog.DiscardHandler), store).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/readiness", nil))

	deadline := <-deadlines
	if until := time.Until(deadline); until <= 0 || until > ReadinessTimeout {
		t.Errorf("check deadline is %s away, want between 0 and %s", until, ReadinessTimeout)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d for a check that returned nil", rec.Code, http.StatusOK)
	}
}

// checkerFunc adapts a function to Checker.
type checkerFunc func(ctx context.Context) error

func (f checkerFunc) Check(ctx context.Context) error { return f(ctx) }
