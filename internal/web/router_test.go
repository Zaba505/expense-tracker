package web

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/Zaba505/expense-tracker/internal/auth"
)

const testOwnerEmail = "owner@example.com"

// stubChecker stands in for the event store: the routing tests care that
// a dependency was consulted, not which one.
type stubChecker struct {
	err    error
	called int
}

func (s *stubChecker) Check(context.Context) error {
	s.called++
	return s.err
}

type stubAuth struct {
	session      auth.Session
	ok           bool
	logoutCalled int
}

func (s *stubAuth) Session(*http.Request) (auth.Session, bool) { return s.session, s.ok }

func (s *stubAuth) LoginHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://accounts.example.test", http.StatusFound)
	})
}

func (s *stubAuth) CallbackHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no login is in progress", http.StatusBadRequest)
	})
}

func (s *stubAuth) LogoutHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.logoutCalled++
		http.SetCookie(w, &http.Cookie{
			Name:   "et_session",
			Value:  "",
			Path:   "/",
			MaxAge: -1,
		})
		http.Redirect(w, r, auth.LoginPath, http.StatusSeeOther)
	})
}

// testHandler is the real routing table, with logs thrown away and a
// dependency that is always healthy.
func testHandler() http.Handler {
	return NewHandler(slog.New(slog.DiscardHandler), &stubChecker{}, testOwnerEmail, testAuthenticator())
}

// testAuthenticator is a real Authenticator with a throwaway signing key
// and no issuer behind it. These tests never complete a sign-in — that is
// the auth package's own test, which stands up a fake Google — so all they
// need is the real thing mounted on the real routes.
func testAuthenticator() *auth.Authenticator {
	authn, err := auth.New(context.Background(), auth.Config{
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		SessionKey:   bytes.Repeat([]byte("k"), auth.MinSessionKeyLen),
	})
	if err != nil {
		panic(err)
	}
	return authn
}

// get runs a request against the real handler in-process, no listener.
func get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	return do(t, http.MethodGet, path)
}

func getWithHandler(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	return doWithHandler(t, h, http.MethodGet, path)
}

func do(t *testing.T, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	return doWithHandler(t, testHandler(), method, path)
}

func doWithHandler(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestLiveness(t *testing.T) {
	t.Parallel()

	rec := get(t, "/health/liveness")

	if rec.Code != http.StatusOK {
		t.Errorf("GET /health/liveness status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Body.String(); got != "ok" {
		t.Errorf("GET /health/liveness body = %q, want %q", got, "ok")
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Errorf("GET /health/liveness Content-Type = %q, want text/plain", got)
	}
}

// TestLiveness_IgnoresDependencies is the whole point of splitting the two
// probes: liveness must pass while Firestore is down, because restarting
// the container — which is what a failed liveness probe causes — cannot
// bring a database back.
func TestLiveness_IgnoresDependencies(t *testing.T) {
	t.Parallel()

	store := &stubChecker{err: errors.New("firestore is down")}
	rec := httptest.NewRecorder()
	NewHandler(slog.New(slog.DiscardHandler), store, testOwnerEmail, testAuthenticator()).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/liveness", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("GET /health/liveness with a dead dependency = %d, want %d", rec.Code, http.StatusOK)
	}
	if store.called != 0 {
		t.Errorf("liveness consulted the event store %d times, want 0", store.called)
	}
}

// TestLiveness_RejectsNonGET pins the method to GET: the probe is a read,
// and ServeMux's "GET /health/liveness" pattern is what enforces it.
func TestLiveness_RejectsNonGET(t *testing.T) {
	t.Parallel()

	rec := do(t, http.MethodPost, "/health/liveness")

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /health/liveness status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestHealthz(t *testing.T) {
	t.Parallel()

	rec := get(t, "/healthz")

	if rec.Code != http.StatusOK {
		t.Errorf("GET /healthz status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Body.String(); got != "ok" {
		t.Errorf("GET /healthz body = %q, want %q", got, "ok")
	}
}

func TestHome_RequiresOwnerSession(t *testing.T) {
	t.Parallel()

	rec := get(t, "/")

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("GET / status = %d, want %d", rec.Code, http.StatusSeeOther)
	}
	if got := rec.Header().Get("Location"); got != auth.LoginPath {
		t.Errorf("GET / redirected to %q, want %q", got, auth.LoginPath)
	}
}

func TestHome_OwnerSession(t *testing.T) {
	t.Parallel()

	authn := &stubAuth{
		session: auth.Session{Email: testOwnerEmail},
		ok:      true,
	}
	rec := getWithHandler(t, NewHandler(slog.New(slog.DiscardHandler), &stubChecker{}, testOwnerEmail, authn), "/")

	if rec.Code != http.StatusOK {
		t.Errorf("GET / status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("GET / Content-Type = %q, want text/html", got)
	}
	if got := rec.Body.String(); !strings.Contains(got, "<!doctype html>") {
		t.Errorf("GET / body is not an HTML document:\n%s", got)
	}
}

func TestHome_NonOwnerSessionIsLoggedOut(t *testing.T) {
	t.Parallel()

	authn := &stubAuth{
		session: auth.Session{Email: "other@example.com"},
		ok:      true,
	}
	rec := getWithHandler(t, NewHandler(slog.New(slog.DiscardHandler), &stubChecker{}, testOwnerEmail, authn), "/")

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("GET / status = %d, want %d", rec.Code, http.StatusSeeOther)
	}
	if got := rec.Header().Get("Location"); got != auth.LoginPath {
		t.Errorf("GET / redirected to %q, want %q", got, auth.LoginPath)
	}
	if authn.logoutCalled != 1 {
		t.Fatalf("logout handler called %d times, want 1", authn.logoutCalled)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == "et_session" && c.MaxAge < 0 {
			return
		}
	}
	t.Fatal("GET / with a non-owner session did not clear the session cookie")
}

// TestAuthRoutesAreMounted pins both halves of the flow onto the routing
// table. The callback is checked with an empty query, which it must
// refuse: there is no login in progress, and a callback that answers
// anything but an error to that would be one that skipped its state check.
func TestAuthRoutesAreMounted(t *testing.T) {
	t.Parallel()

	if code := get(t, auth.LoginPath).Code; code != http.StatusFound {
		t.Errorf("GET %s = %d, want %d (a redirect to Google)", auth.LoginPath, code, http.StatusFound)
	}
	if code := get(t, auth.CallbackPath).Code; code != http.StatusBadRequest {
		t.Errorf("GET %s with no login in progress = %d, want %d",
			auth.CallbackPath, code, http.StatusBadRequest)
	}
}

func TestLogoutRouteIsMounted(t *testing.T) {
	t.Parallel()

	rec := get(t, auth.LogoutPath)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("GET %s = %d, want %d", auth.LogoutPath, rec.Code, http.StatusSeeOther)
	}
	if got := rec.Header().Get("Location"); got != auth.LoginPath {
		t.Errorf("GET %s redirected to %q, want %q", auth.LogoutPath, got, auth.LoginPath)
	}
}

// TestUnknownPathIs404 guards the "/{$}" root pattern for an authorized
// caller: registered as a bare "/", the home page would match every
// unrouted path instead.
func TestUnknownPathIs404(t *testing.T) {
	t.Parallel()

	authn := &stubAuth{
		session: auth.Session{Email: testOwnerEmail},
		ok:      true,
	}
	rec := getWithHandler(t, NewHandler(slog.New(slog.DiscardHandler), &stubChecker{}, testOwnerEmail, authn), "/no-such-page")

	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /no-such-page status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestStaticAssets_RequireOwnerSession(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path string
	}{
		{path: "/static/app.css"},
		{path: "/static/htmx.min.js"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			rec := get(t, tt.path)

			if rec.Code != http.StatusSeeOther {
				t.Fatalf("GET %s status = %d, want %d", tt.path, rec.Code, http.StatusSeeOther)
			}
			if got := rec.Header().Get("Location"); got != auth.LoginPath {
				t.Errorf("GET %s redirected to %q, want %q", tt.path, got, auth.LoginPath)
			}
		})
	}
}

// assetRef finds every same-origin asset the page pulls in: the stylesheet
// href and the script src.
var assetRef = regexp.MustCompile(`(?:href|src)="(/static/[^"]+)"`)

// TestHomeAssetsAreServed is the drift test between the layout and the
// router. Renaming an asset, or moving where assets are mounted, breaks
// the page silently — the HTML still renders, it just arrives unstyled and
// without htmx. So: fetch what the page actually asks for and require a
// 200 for each.
func TestHomeAssetsAreServed(t *testing.T) {
	t.Parallel()

	authn := &stubAuth{
		session: auth.Session{Email: testOwnerEmail},
		ok:      true,
	}
	handler := NewHandler(slog.New(slog.DiscardHandler), &stubChecker{}, testOwnerEmail, authn)
	home := getWithHandler(t, handler, "/").Body.String()

	refs := assetRef.FindAllStringSubmatch(home, -1)
	if len(refs) < 2 {
		t.Fatalf("home page references %d assets, want the stylesheet and htmx:\n%s", len(refs), home)
	}

	for _, ref := range refs {
		path := ref[1]
		if code := getWithHandler(t, handler, path).Code; code != http.StatusOK {
			t.Errorf("home page references %s, but GET %s = %d, want %d", path, path, code, http.StatusOK)
		}
	}
}
