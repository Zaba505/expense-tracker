package web

import (
	"io"
	"log/slog"
	"net/http"

	"github.com/a-h/templ"

	"github.com/Zaba505/expense-tracker/internal/view"
)

// NewHandler returns the application's HTTP handler: every route, wrapped
// in the shared middleware. It is the whole HTTP surface of the app, so a
// test can exercise the real routing table with httptest and no listener.
func NewHandler(logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()

	// "/{$}" matches only the root path; a bare "/" would make the home
	// page a catch-all and swallow every 404.
	mux.Handle("GET /{$}", templ.Handler(view.Home()))
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.Handle("GET "+view.AssetPrefix, view.AssetHandler())

	return logRequests(logger, mux)
}

// handleHealthz is the liveness probe. It reports that the process is up
// and serving, nothing more: it deliberately does not reach out to
// Firestore, so a dependency being slow or down cannot get a healthy
// container killed and restarted into the same failure.
func handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ok")
}
