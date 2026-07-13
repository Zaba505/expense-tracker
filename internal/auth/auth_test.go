package auth

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const (
	testClientID     = "client-id.apps.googleusercontent.com"
	testClientSecret = "client-secret"
)

// testSessionKey is a valid signing key: what SESSION_KEY decodes to.
func testSessionKey() []byte {
	return bytes.Repeat([]byte("k"), MinSessionKeyLen)
}

// login runs the login handler and returns the state and nonce it minted,
// plus the cookie it stashed them in — everything the browser would carry
// forward into the callback.
func login(t *testing.T, a *Authenticator) (state, nonce string, flow *http.Cookie) {
	t.Helper()

	rec := httptest.NewRecorder()
	a.LoginHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, LoginPath, nil))

	if rec.Code != http.StatusFound {
		t.Fatalf("GET %s = %d, want %d", LoginPath, rec.Code, http.StatusFound)
	}

	redirect, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("login redirected to an unparseable URL: %v", err)
	}

	flow = findCookie(t, rec.Result().Cookies(), flowCookie)
	return redirect.Query().Get("state"), redirect.Query().Get("nonce"), flow
}

// callback drives the callback handler with the given query and cookies.
func callback(t *testing.T, a *Authenticator, query url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()

	r := httptest.NewRequest(http.MethodGet, CallbackPath+"?"+query.Encode(), nil)
	for _, c := range cookies {
		if c != nil {
			r.AddCookie(c)
		}
	}

	rec := httptest.NewRecorder()
	a.CallbackHandler().ServeHTTP(rec, r)
	return rec
}

// findCookie fails the test if the response did not set the named cookie.
func findCookie(t *testing.T, cookies []*http.Cookie, name string) *http.Cookie {
	t.Helper()

	for _, c := range cookies {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("response set no %q cookie (it set %d cookies)", name, len(cookies))
	return nil
}

// hasCookie reports whether the response set the named cookie to a
// non-empty value — an expiring cookie (Max-Age < 0) does not count.
func hasCookie(cookies []*http.Cookie, name string) bool {
	for _, c := range cookies {
		if c.Name == name && c.Value != "" && c.MaxAge >= 0 {
			return true
		}
	}
	return false
}

func TestLogin_RedirectsToGoogle(t *testing.T) {
	t.Parallel()

	google := newFakeGoogle(t)
	a := google.authenticator(t, nil)

	rec := httptest.NewRecorder()
	a.LoginHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, LoginPath, nil))

	if rec.Code != http.StatusFound {
		t.Fatalf("GET %s = %d, want %d", LoginPath, rec.Code, http.StatusFound)
	}

	redirect, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("Location is not a URL: %v", err)
	}
	if want := google.server.URL + "/authorize"; !strings.HasPrefix(redirect.String(), want) {
		t.Errorf("login redirected to %s, want the issuer's authorization endpoint %s", redirect, want)
	}

	query := redirect.Query()
	want := map[string]string{
		"client_id":     testClientID,
		"response_type": "code",
		// httptest.NewRequest serves from example.com over plain http, so
		// this is the origin the request itself arrived at — which is the
		// point: the redirect URI follows the request, not a constant.
		"redirect_uri": "http://example.com" + CallbackPath,
		"scope":        "openid email",
	}
	for param, value := range want {
		if got := query.Get(param); got != value {
			t.Errorf("authorization URL %s = %q, want %q", param, got, value)
		}
	}

	// State and nonce are the two things the callback checks. A flow that
	// sent neither would still redirect, still come back, and still sign
	// somebody in — which is the bug this asserts against.
	if query.Get("state") == "" {
		t.Error("authorization URL carries no state — the callback would be forgeable")
	}
	if query.Get("nonce") == "" {
		t.Error("authorization URL carries no nonce — an old id token would be replayable")
	}

	cookie := findCookie(t, rec.Result().Cookies(), flowCookie)
	if !cookie.HttpOnly {
		t.Error("login cookie is readable by scripts")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		// Strict would withhold the cookie from the callback — a top-level
		// navigation from accounts.google.com — and every sign-in would
		// fail with "no login is in progress".
		t.Errorf("login cookie SameSite = %v, want Lax", cookie.SameSite)
	}
	if cookie.Path != "/auth" {
		t.Errorf("login cookie Path = %q, want %q", cookie.Path, "/auth")
	}
	if cookie.Value == query.Get("state") || cookie.Value == query.Get("nonce") {
		t.Error("login cookie is the raw state or nonce, not a signed payload")
	}
}

// TestLogin_HTTPS covers what changes when the app is actually deployed:
// Cloud Run terminates TLS and forwards plain HTTP with X-Forwarded-Proto,
// so that header is the only thing telling the app it is on https. Get it
// wrong and the cookies go out without Secure and the redirect URI comes
// out as http:// — one that Google is not registered to redirect to.
func TestLogin_HTTPS(t *testing.T) {
	t.Parallel()

	a := newFakeGoogle(t).authenticator(t, nil)

	r := httptest.NewRequest(http.MethodGet, LoginPath, nil)
	r.Host = "expenses.example.com"
	r.Header.Set("X-Forwarded-Proto", "https")

	rec := httptest.NewRecorder()
	a.LoginHandler().ServeHTTP(rec, r)

	redirect, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("Location is not a URL: %v", err)
	}
	if got, want := redirect.Query().Get("redirect_uri"), "https://expenses.example.com"+CallbackPath; got != want {
		t.Errorf("redirect_uri = %q, want %q", got, want)
	}
	if cookie := findCookie(t, rec.Result().Cookies(), flowCookie); !cookie.Secure {
		t.Error("login cookie is not Secure on an https request — it would travel in the clear")
	}
}

// TestLogin_BaseURL pins the override: behind a custom domain the request's
// own host is not necessarily the origin Google was told about.
func TestLogin_BaseURL(t *testing.T) {
	t.Parallel()

	google := newFakeGoogle(t)
	a := google.authenticator(t, nil)
	a.baseURL = "https://expenses.example.com"

	rec := httptest.NewRecorder()
	a.LoginHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, LoginPath, nil))

	redirect, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("Location is not a URL: %v", err)
	}
	if got, want := redirect.Query().Get("redirect_uri"), "https://expenses.example.com"+CallbackPath; got != want {
		t.Errorf("redirect_uri = %q, want the configured base URL %q", got, want)
	}
}

// TestSignIn is the whole story end to end: log in, come back with the
// code Google would hand over, and end up with a session that says who
// you are.
func TestSignIn(t *testing.T) {
	t.Parallel()

	google := newFakeGoogle(t)
	a := google.authenticator(t, nil)

	state, nonce, flow := login(t, a)

	// Google echoes the nonce into the ID token; that is what the callback
	// checks it against.
	google.setClaim("nonce", nonce)

	rec := callback(t, a, url.Values{
		"state": {state},
		"code":  {"the-authorization-code"},
	}, flow)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("callback = %d (%s), want %d", rec.Code, strings.TrimSpace(rec.Body.String()), http.StatusSeeOther)
	}
	if got := rec.Header().Get("Location"); got != "/" {
		t.Errorf("callback redirected to %q, want %q", got, "/")
	}

	// The code really was redeemed, with this app's credentials, against
	// the same redirect URI the login used — Google rejects the exchange
	// otherwise, and a test that skipped this would not notice.
	form := google.postedToTokenEndpoint()
	if form == nil {
		t.Fatal("the app never posted to the token endpoint — where did the session come from?")
	}
	if got := form.Get("code"); got != "the-authorization-code" {
		t.Errorf("token request code = %q, want the code from the callback", got)
	}
	if got := form.Get("grant_type"); got != "authorization_code" {
		t.Errorf("token request grant_type = %q, want authorization_code", got)
	}
	if got := form.Get("redirect_uri"); got != "http://example.com"+CallbackPath {
		t.Errorf("token request redirect_uri = %q, want the one the login used", got)
	}

	// And the session it handed back is a session this Authenticator will
	// accept on a later request, which is the only property that matters.
	session := findCookie(t, rec.Result().Cookies(), sessionCookie)
	next := httptest.NewRequest(http.MethodGet, "/", nil)
	next.AddCookie(session)

	got, ok := a.Session(next)
	if !ok {
		t.Fatal("the session cookie the callback set is not one the app accepts")
	}
	if got.Email != "owner@example.com" {
		t.Errorf("session email = %q, want %q", got.Email, "owner@example.com")
	}
	if got.Subject != "google-subject-id" {
		t.Errorf("session subject = %q, want the sub claim", got.Subject)
	}
	if got.ExpiresAt.IsZero() {
		t.Error("session never expires")
	}

	// The flow cookie is spent: leaving it on the browser would leave the
	// state and nonce usable for a second callback.
	if hasCookie(rec.Result().Cookies(), flowCookie) {
		t.Error("callback left the login cookie in place — a replayed callback would still find its state")
	}
}

// TestCallback_Rejects is the security surface of this package, one refusal
// per row. Each case is a way an attacker or a broken provider could try to
// get a session out of the callback, and none of them may.
func TestCallback_Rejects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string

		// spoil mutates the flow after a successful login: the query the
		// callback receives, the cookie it carries, and the issuer that
		// will answer the token request.
		spoil func(t *testing.T, g *fakeGoogle, a *Authenticator, query url.Values, flow *http.Cookie) *http.Cookie

		want int
	}{
		{
			name: "no login in progress",
			// The bare callback URL, with no cookie. This is what an
			// attacker replaying a captured callback has, and the state
			// cookie is exactly what they cannot produce.
			spoil: func(_ *testing.T, _ *fakeGoogle, _ *Authenticator, _ url.Values, _ *http.Cookie) *http.Cookie {
				return nil
			},
			want: http.StatusBadRequest,
		},
		{
			name: "state does not match",
			// Session fixation: the attacker feeds the owner's browser a
			// callback carrying the attacker's own code, hoping to log the
			// owner into the attacker's account.
			spoil: func(_ *testing.T, _ *fakeGoogle, _ *Authenticator, query url.Values, flow *http.Cookie) *http.Cookie {
				query.Set("state", "a-state-we-never-issued")
				return flow
			},
			want: http.StatusBadRequest,
		},
		{
			name: "flow cookie signed with another key",
			// A forged cookie asserting a state of the attacker's choosing.
			// The HMAC is what makes it unforgeable.
			spoil: func(t *testing.T, _ *fakeGoogle, _ *Authenticator, query url.Values, _ *http.Cookie) *http.Cookie {
				forger := codec{key: bytes.Repeat([]byte("x"), MinSessionKeyLen)}
				value, err := forger.seal(loginFlow{
					State:     "attacker-state",
					Nonce:     "attacker-nonce",
					ExpiresAt: time.Now().Add(flowTTL),
				})
				if err != nil {
					t.Fatal(err)
				}
				query.Set("state", "attacker-state")
				return &http.Cookie{Name: flowCookie, Value: value}
			},
			want: http.StatusBadRequest,
		},
		{
			name: "login expired",
			spoil: func(_ *testing.T, _ *fakeGoogle, a *Authenticator, _ url.Values, flow *http.Cookie) *http.Cookie {
				// The clock moves past the flow's lifetime between the
				// redirect and the callback.
				a.now = func() time.Time { return time.Now().Add(flowTTL + time.Minute) }
				return flow
			},
			want: http.StatusBadRequest,
		},
		{
			name: "google declined",
			spoil: func(_ *testing.T, _ *fakeGoogle, _ *Authenticator, query url.Values, flow *http.Cookie) *http.Cookie {
				query.Del("code")
				query.Set("error", "access_denied")
				return flow
			},
			want: http.StatusBadRequest,
		},
		{
			name: "no authorization code",
			spoil: func(_ *testing.T, _ *fakeGoogle, _ *Authenticator, query url.Values, flow *http.Cookie) *http.Cookie {
				query.Del("code")
				return flow
			},
			want: http.StatusBadRequest,
		},
		{
			name: "token endpoint refuses",
			spoil: func(_ *testing.T, g *fakeGoogle, _ *Authenticator, _ url.Values, flow *http.Cookie) *http.Cookie {
				g.tokenStatus = http.StatusInternalServerError
				return flow
			},
			// Not the caller's fault: the request was well-formed and came
			// from a flow we started.
			want: http.StatusBadGateway,
		},
		{
			name: "no id token in the response",
			spoil: func(_ *testing.T, g *fakeGoogle, _ *Authenticator, _ url.Values, flow *http.Cookie) *http.Cookie {
				g.omitIDToken = true
				return flow
			},
			want: http.StatusBadGateway,
		},
		{
			name: "id token signed by somebody else",
			// The whole reason the signature is checked: without it, an
			// ID token is a string, and a string can say anything.
			spoil: func(_ *testing.T, g *fakeGoogle, _ *Authenticator, _ url.Values, flow *http.Cookie) *http.Cookie {
				g.signWith = attackerKey()
				return flow
			},
			want: http.StatusBadGateway,
		},
		{
			name: "id token minted for another app",
			// A valid Google token, signed by Google, for somebody else's
			// OAuth client. The aud check is what stops it being replayed
			// here.
			spoil: func(_ *testing.T, g *fakeGoogle, _ *Authenticator, _ url.Values, flow *http.Cookie) *http.Cookie {
				g.setClaim("aud", "some-other-app.apps.googleusercontent.com")
				return flow
			},
			want: http.StatusBadGateway,
		},
		{
			name: "id token from another issuer",
			spoil: func(_ *testing.T, g *fakeGoogle, _ *Authenticator, _ url.Values, flow *http.Cookie) *http.Cookie {
				g.setClaim("iss", "https://accounts.evil.example")
				return flow
			},
			want: http.StatusBadGateway,
		},
		{
			name: "id token has expired",
			spoil: func(_ *testing.T, g *fakeGoogle, _ *Authenticator, _ url.Values, flow *http.Cookie) *http.Cookie {
				g.setClaim("exp", time.Now().Add(-time.Hour).Unix())
				return flow
			},
			want: http.StatusBadGateway,
		},
		{
			name: "nonce does not match",
			// A token minted for a different login of the same user,
			// captured and replayed into this one.
			spoil: func(_ *testing.T, g *fakeGoogle, _ *Authenticator, _ url.Values, flow *http.Cookie) *http.Cookie {
				g.setClaim("nonce", "the-nonce-of-some-other-login")
				return flow
			},
			want: http.StatusBadRequest,
		},
		{
			name: "email is not verified",
			// An unverified email is a string the account holder typed. The
			// allowlist (#14) compares emails, so accepting one would let
			// anybody who can name the owner's address be the owner.
			spoil: func(_ *testing.T, g *fakeGoogle, _ *Authenticator, _ url.Values, flow *http.Cookie) *http.Cookie {
				g.setClaim("email_verified", false)
				return flow
			},
			want: http.StatusForbidden,
		},
		{
			name: "no email at all",
			spoil: func(_ *testing.T, g *fakeGoogle, _ *Authenticator, _ url.Values, flow *http.Cookie) *http.Cookie {
				g.setClaim("email", "")
				return flow
			},
			want: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			google := newFakeGoogle(t)
			a := google.authenticator(t, nil)

			state, nonce, flow := login(t, a)
			google.setClaim("nonce", nonce)

			query := url.Values{
				"state": {state},
				"code":  {"the-authorization-code"},
			}
			flow = tt.spoil(t, google, a, query, flow)

			rec := callback(t, a, query, flow)

			if rec.Code != tt.want {
				t.Errorf("callback = %d, want %d (body: %s)",
					rec.Code, tt.want, strings.TrimSpace(rec.Body.String()))
			}

			// The one thing that must be true of every refusal, whatever
			// its status: nobody got a session out of it.
			if hasCookie(rec.Result().Cookies(), sessionCookie) {
				t.Error("a refused sign-in still handed out a session cookie")
			}
		})
	}
}

// TestCallback_DoesNotLeak keeps the failure details in the log, where the
// owner can read them, and out of the response, where an attacker probing
// the callback would learn which of their guesses was closest.
func TestCallback_DoesNotLeak(t *testing.T) {
	t.Parallel()

	google := newFakeGoogle(t)
	a := google.authenticator(t, nil)

	state, _, flow := login(t, a)
	google.setClaim("nonce", "the-nonce-of-some-other-login")

	rec := callback(t, a, url.Values{
		"state": {state},
		"code":  {"the-authorization-code"},
	}, flow)

	body := rec.Body.String()
	for _, secret := range []string{state, testClientSecret, testClientID, google.server.URL} {
		if strings.Contains(body, secret) {
			t.Errorf("the failure response quotes %q back to the caller:\n%s", secret, body)
		}
	}
}
