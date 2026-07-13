package web

import (
	"log/slog"
	"net/http"

	"github.com/Zaba505/expense-tracker/internal/auth"
)

type authenticator interface {
	Session(*http.Request) (auth.Session, bool)
	ClearSession(http.ResponseWriter, *http.Request)
	LoginHandler() http.Handler
	CallbackHandler() http.Handler
	LogoutHandler() http.Handler
}

// requireOwner is the application's one authorization gate. Today it enforces
// the owner allowlist by requiring a session whose email matches OWNER_EMAIL;
// if the app later switches to IAP, this middleware is the one place to swap
// the session check for whatever assertion IAP provides.
func requireOwner(logger *slog.Logger, ownerEmail string, authn authenticator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPublicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		session, ok := authn.Session(r)
		if ok && session.Email == ownerEmail {
			next.ServeHTTP(w, r)
			return
		}

		if ok {
			logger.WarnContext(r.Context(), "rejecting non-owner session",
				slog.String("email", session.Email),
			)
			authn.ClearSession(w, r)
			http.Redirect(w, r, auth.LoginPath, http.StatusSeeOther)
			return
		}

		http.Redirect(w, r, auth.LoginPath, http.StatusSeeOther)
	})
}

func isPublicPath(path string) bool {
	switch path {
	case auth.LoginPath, auth.CallbackPath, auth.LogoutPath, healthzPath, livenessPath, readinessPath:
		return true
	default:
		return false
	}
}
