package web

import (
	"log/slog"
	"net/http"

	"github.com/Zaba505/expense-tracker/internal/auth"
	"github.com/Zaba505/expense-tracker/internal/view"
)

// NewHandler returns the application's HTTP handler: every route, wrapped
// in the shared middleware. It is the whole HTTP surface of the app, so a
// test can exercise the real routing table with httptest and no listener.
//
// store backs the readiness probe; it is the app's one hard dependency.
// ownerEmail is the one Google account allowed past the authorization
// middleware. authn serves the Google Sign-In flow and reads the sessions it
// hands out.
func NewHandler(logger *slog.Logger, store Checker, ownerEmail string, authn authenticator) http.Handler {
	mux := http.NewServeMux()

	// "/{$}" matches only the root path; a bare "/" would make the home
	// page a catch-all and swallow every 404.
	mux.Handle("GET /{$}", handleHome(logger, authn))

	// The sign-in flow: /auth/login sends the browser to Google,
	// /auth/callback is the URI Google is registered to send it back to.
	// Both are GETs, because both are navigations.
	mux.Handle("GET "+auth.LoginPath, authn.LoginHandler())
	mux.Handle("GET "+auth.CallbackPath, authn.CallbackHandler())
	mux.Handle("GET "+auth.LogoutPath, authn.LogoutHandler())

	// Two probes, because they answer different questions and the platform
	// does different things with the answers: liveness says the process is
	// up (fail it and Cloud Run restarts the container), readiness says the
	// process can serve (fail it and Cloud Run just holds traffic back).
	// Folding Firestore into liveness would turn a database blip into a
	// restart loop.
	mux.HandleFunc("GET /healthz", handleLiveness)
	mux.HandleFunc("GET /health/liveness", handleLiveness)
	mux.Handle("GET /health/readiness", handleReadiness(logger, store))

	mux.Handle("GET "+view.AssetPrefix, view.AssetHandler())

	return logRequests(logger, requireOwner(logger, ownerEmail, authn, mux))
}

// handleHome renders the home page, told who is looking at it. Authorization
// already happened in the shared middleware, so the only job here is to render
// the page and show which account made it through.
func handleHome(logger *slog.Logger, authn authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var email string
		if session, ok := authn.Session(r); ok {
			email = session.Email
		}

		// Logged, not answered with a 500: templ streams straight to the
		// ResponseWriter, so by the time a render can fail the status line
		// and part of the page are already on their way to the browser, and
		// http.Error would only append its text to a half-written document.
		// A truncated page the log explains beats a lie about it being whole.
		if err := view.Home(email).Render(r.Context(), w); err != nil {
			logger.ErrorContext(r.Context(), "rendering the home page",
				slog.Any("error", err),
			)
		}
	}
}
