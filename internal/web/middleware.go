package web

import (
	"log/slog"
	"net/http"
	"time"
)

// logRequests emits one structured line per request. On Cloud Run these
// land in Cloud Logging as JSON, so a bad deploy is diagnosable from the
// log stream alone.
func logRequests(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		logger.InfoContext(r.Context(), "request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", rec.status),
			slog.Duration("duration", time.Since(start)),
		)
	})
}

// statusRecorder remembers the status code passed to WriteHeader so it can
// be logged after the handler has run. A handler that writes a body without
// calling WriteHeader implicitly sends 200, which is the zero state here.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}
