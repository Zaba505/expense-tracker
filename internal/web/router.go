package web

import (
	"log/slog"
	"net/http"

	"github.com/a-h/templ"

	"github.com/Zaba505/expense-tracker/internal/view"
)

// NewHandler returns the application's HTTP handler: every route, wrapped
// in the shared middleware. It is the whole HTTP surface of the app, so a
// test can exercise the real routing table with httptest and no listener.
//
// store backs the readiness probe; it is the app's one hard dependency.
func NewHandler(logger *slog.Logger, store Checker) http.Handler {
	mux := http.NewServeMux()

	// "/{$}" matches only the root path; a bare "/" would make the home
	// page a catch-all and swallow every 404.
	mux.Handle("GET /{$}", templ.Handler(view.Home()))

	// Two probes, because they answer different questions and the platform
	// does different things with the answers: liveness says the process is
	// up (fail it and Cloud Run restarts the container), readiness says the
	// process can serve (fail it and Cloud Run just holds traffic back).
	// Folding Firestore into liveness would turn a database blip into a
	// restart loop.
	mux.HandleFunc("GET /health/liveness", handleLiveness)
	mux.Handle("GET /health/readiness", handleReadiness(logger, store))

	mux.Handle("GET "+view.AssetPrefix, view.AssetHandler())

	return logRequests(logger, mux)
}
