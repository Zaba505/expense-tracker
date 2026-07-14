package web

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/Zaba505/expense-tracker/internal/auth"
	"github.com/Zaba505/expense-tracker/internal/domain"
	"github.com/Zaba505/expense-tracker/internal/eventlog"
	"github.com/Zaba505/expense-tracker/internal/view"
)

// Store is the event log as the web layer needs it: a log to append to and to
// fold, and a dependency the readiness probe can check.
//
// It is one interface rather than two parameters because it is one thing — the
// Firestore-backed eventlog.Store satisfies all of it — and because the two
// halves must be the same log: a readiness probe reporting on a database the
// handlers do not write to would report health the app does not have. The
// halves stay apart where they are used, though: handleReadiness takes only a
// Checker and the handlers take only an eventlog.EventStore, so neither can
// reach past what it needs.
type Store interface {
	eventlog.EventStore
	Checker
}

// NewHandler returns the application's HTTP handler: every route, wrapped
// in the shared middleware. It is the whole HTTP surface of the app, so a
// test can exercise the real routing table with httptest and no listener.
//
// store is the event log — the handlers append to it and fold it, and the
// readiness probe checks it. ownerEmail is the one Google account allowed past
// the authorization middleware. authn serves the Google Sign-In flow and reads
// the sessions it hands out.
func NewHandler(logger *slog.Logger, store Store, ownerEmail string, authn authenticator) http.Handler {
	mux := http.NewServeMux()

	// "/{$}" matches only the root path; a bare "/" would make the home
	// page a catch-all and swallow every 404.
	mux.Handle("GET /{$}", handleHome(logger, store, authn))
	mux.Handle("GET /month/{month}", handleMonth(logger, store, authn))

	// The entry form's hx-post, mounted on the very path the form is rendered
	// with, so the route and the markup cannot drift apart.
	mux.Handle("POST "+view.EntriesPath, handleEntry(logger, store))

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
	mux.HandleFunc("GET "+healthzPath, handleLiveness)
	mux.HandleFunc("GET "+livenessPath, handleLiveness)
	mux.Handle("GET "+readinessPath, handleReadiness(logger, store))

	mux.Handle("GET "+view.AssetPrefix, view.AssetHandler())

	return logRequests(logger, requireOwner(logger, ownerEmail, authn, mux))
}

// handleHome renders the home page: the current month's panel, and who is
// looking at it. Authorization already happened in the shared middleware, so
// the only jobs here are to fold the log and render it.
//
// The month is the current one in UTC, because the log's months are UTC's (see
// domain.Month). An owner in the Americas opening the page late on the 31st is
// shown next month for those few hours — and picks the month they meant in the
// form, which is the same field they would use to record last March.
func handleHome(logger *slog.Logger, log eventlog.EventStore, authn authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		renderMonthPage(w, r, logger, log, authn, domain.Month(time.Now()))
	}
}

// handleMonth renders one requested month of the log.
//
// The month comes from the path rather than the clock, which is what makes the
// route an addressable projection: opening /month/2026-07 shows July 2026 even
// when the current month is years later. A malformed month is not "an empty
// month"; it is not a month at all, so the route answers 404 rather than
// pretending the log says something about it.
func handleMonth(logger *slog.Logger, log eventlog.EventStore, authn authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		month := r.PathValue("month")
		if !domain.ValidMonth(month) {
			http.NotFound(w, r)
			return
		}
		renderMonthPage(w, r, logger, log, authn, month)
	}
}

func renderMonthPage(w http.ResponseWriter, r *http.Request, logger *slog.Logger, log eventlog.EventStore, authn authenticator, month string) {
	var email string
	if session, ok := authn.Session(r); ok {
		email = session.Email
	}

	// The fold happens before a byte is written, so a log that cannot be
	// read is an honest 500 — unlike the render below, which cannot be.
	panel, err := loadPanel(r.Context(), log, month, view.NewForm(month))
	if err != nil {
		logger.ErrorContext(r.Context(), "folding the log for the home page",
			slog.String("month", month),
			slog.Any("error", err),
		)
		http.Error(w, "the expenses could not be loaded", http.StatusInternalServerError)
		return
	}

	// Logged, not answered with a 500: templ streams straight to the
	// ResponseWriter, so by the time a render can fail the status line
	// and part of the page are already on their way to the browser, and
	// http.Error would only append its text to a half-written document.
	// A truncated page the log explains beats a lie about it being whole.
	if err := view.Home(email, panel).Render(r.Context(), w); err != nil {
		logger.ErrorContext(r.Context(), "rendering the home page",
			slog.Any("error", err),
		)
	}
}
