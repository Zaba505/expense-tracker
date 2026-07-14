package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"
)

const (
	healthzPath   = "/healthz"
	livenessPath  = "/health/liveness"
	readinessPath = "/health/readiness"
)

// ReadinessTimeout bounds the dependency check behind GET
// /health/readiness. An unreachable Firestore fails by hanging rather
// than by refusing, so without a deadline a probe would wait as long as
// the platform lets it and then report nothing at all. Well under Cloud
// Run's probe timeout, so the answer is ours to give, not the platform's
// to time out.
const ReadinessTimeout = 3 * time.Second

// Checker reports whether a dependency is reachable, returning nil when
// it is. It is the seam between the HTTP layer and the event store: the
// router needs to know a dependency is healthy, not what it is.
type Checker interface {
	Check(ctx context.Context) error
}

// handleLiveness is the liveness probe: it answers for the process and
// nothing else. It deliberately does not touch Firestore — a liveness
// failure means "restart me", and restarting the container cannot fix a
// dependency that is down. That is what readiness is for: it takes the
// instance out of rotation without killing it.
func handleLiveness(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ok")
}

// readiness is the body of GET /health/readiness.
type readiness struct {
	Status    string `json:"status"`
	Firestore string `json:"firestore"`
	LatencyMs int64  `json:"latency_ms"`
}

// handleReadiness reports whether the app can actually serve traffic: it
// round-trips a document through Firestore and answers 503 when it
// cannot. Cloud Run holds requests off an instance that fails this,
// rather than restarting it.
//
// The reason for a failure goes to the log, not to the response: this
// endpoint is unauthenticated, and a Firestore error names the project
// and the database.
func handleReadiness(logger *slog.Logger, store Checker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), ReadinessTimeout)
		defer cancel()

		start := time.Now()
		err := store.Check(ctx)
		latency := time.Since(start)

		body := readiness{
			Status:    "ok",
			Firestore: "ok",
			LatencyMs: latency.Milliseconds(),
		}
		status := http.StatusOK

		if err != nil {
			// Logged against the request context rather than ctx: when the
			// failure is the deadline, ctx is already done, and a handler
			// that does anything with the context it is handed can drop the
			// very record that says so. Nothing is lost by using the parent —
			// ctx derives from it, so the request-scoped values are the same.
			logger.ErrorContext(r.Context(), "readiness check failed",
				slog.String("dependency", "firestore"),
				slog.Duration("latency", latency),
				slog.Any("error", err),
			)
			body = readiness{
				Status:    "unavailable",
				Firestore: "unreachable",
				LatencyMs: latency.Milliseconds(),
			}
			status = http.StatusServiceUnavailable
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		// Probes must never be served from a cache, or an instance would
		// keep reporting the health it had, not the health it has.
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}
}
