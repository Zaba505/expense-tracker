package auth

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// sessionOf seals s into a cookie value with the test key, the way the
// callback would.
func sessionOf(t *testing.T, s Session) string {
	t.Helper()

	value, err := codec{key: testSessionKey()}.seal(s)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	return value
}

// requestWithSession returns a request carrying the given cookie value.
func requestWithSession(value string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: value})
	return r
}

func TestSession_Accepts(t *testing.T) {
	t.Parallel()

	a := newFakeGoogle(t).authenticator(t, nil)
	want := Session{
		Email:     "owner@example.com",
		Subject:   "google-subject-id",
		ExpiresAt: time.Now().Add(SessionTTL).Truncate(time.Second),
	}

	got, ok := a.Session(requestWithSession(sessionOf(t, want)))
	if !ok {
		t.Fatal("Session() = false for a cookie this Authenticator signed")
	}
	if got.Email != want.Email || got.Subject != want.Subject {
		t.Errorf("Session() = %+v, want %+v", got, want)
	}
	if !got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Errorf("Session() expiry = %s, want %s (the payload's, not the browser's)",
			got.ExpiresAt, want.ExpiresAt)
	}
}

// TestSession_Rejects is the whole security value of a stateless session:
// every cookie the app did not sign, or signed too long ago, must read as
// no session at all. There is no store to check against — this signature
// is the only thing between a cookie and an identity.
func TestSession_Rejects(t *testing.T) {
	t.Parallel()

	valid := Session{
		Email:     "owner@example.com",
		Subject:   "google-subject-id",
		ExpiresAt: time.Now().Add(SessionTTL),
	}

	// A cookie sealed with a different key: what an attacker who does not
	// hold SESSION_KEY can produce, and what a key rotation leaves behind.
	forged, err := codec{key: bytes.Repeat([]byte("x"), MinSessionKeyLen)}.seal(valid)
	if err != nil {
		t.Fatal(err)
	}

	authentic := sessionOf(t, valid)
	payload, mac, _ := strings.Cut(authentic, ".")

	tests := []struct {
		name  string
		value string
	}{
		{name: "empty", value: ""},
		{name: "not a cookie we made", value: "gibberish"},
		{name: "no signature", value: payload},
		{name: "signed with another key", value: forged},
		// The forgery that matters: keep our signature, swap the payload
		// under it. The MAC covers the payload, so it cannot survive.
		{name: "payload swapped under the signature", value: sessionPayload(t, Session{
			Email:     "attacker@example.com",
			ExpiresAt: time.Now().Add(SessionTTL),
		}) + "." + mac},
		// An expiry pushed out by hand is the same forgery: it changes the
		// payload the MAC was computed over.
		{name: "expiry extended by hand", value: sessionPayload(t, Session{
			Email:     valid.Email,
			Subject:   valid.Subject,
			ExpiresAt: time.Now().Add(100 * 365 * 24 * time.Hour),
		}) + "." + mac},
		{name: "truncated", value: authentic[:len(authentic)-4]},
	}

	a := newFakeGoogle(t).authenticator(t, nil)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, ok := a.Session(requestWithSession(tt.value)); ok {
				t.Errorf("Session() accepted a cookie it should not have: %q", tt.value)
			}
		})
	}
}

// TestSession_NoCookie: no cookie, no session, no error.
func TestSession_NoCookie(t *testing.T) {
	t.Parallel()

	a := newFakeGoogle(t).authenticator(t, nil)

	if _, ok := a.Session(httptest.NewRequest(http.MethodGet, "/", nil)); ok {
		t.Error("Session() = true for a request carrying no cookie")
	}
}

// TestSession_Expires is why the expiry lives inside the signed payload
// rather than in the cookie's Max-Age: Max-Age is enforced by the browser,
// and a client that simply keeps sending an expired cookie has to be told
// no by the server.
func TestSession_Expires(t *testing.T) {
	t.Parallel()

	a := newFakeGoogle(t).authenticator(t, nil)
	value := sessionOf(t, Session{
		Email:     "owner@example.com",
		ExpiresAt: time.Now().Add(SessionTTL),
	})

	if _, ok := a.Session(requestWithSession(value)); !ok {
		t.Fatal("Session() = false for a fresh session")
	}

	// The same cookie, a day and a minute later.
	a.now = func() time.Time { return time.Now().Add(SessionTTL + time.Minute) }

	if _, ok := a.Session(requestWithSession(value)); ok {
		t.Error("Session() still accepts a cookie past its expiry — the browser's Max-Age is not a check")
	}
}

// TestSetSession_CookieAttributes pins what the browser is told to do with
// the cookie. Each attribute here is one line in cookie() and one class of
// bug: a session readable by any script on the page, one sent along with a
// cross-site request, or one that travels in the clear.
func TestSetSession_CookieAttributes(t *testing.T) {
	t.Parallel()

	a := newFakeGoogle(t).authenticator(t, nil)

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Forwarded-Proto", "https") // as Cloud Run sends it

	err := a.setSession(rec, r, Session{
		Email:     "owner@example.com",
		ExpiresAt: time.Now().Add(SessionTTL),
	})
	if err != nil {
		t.Fatalf("setSession() error = %v", err)
	}

	cookie := findCookie(t, rec.Result().Cookies(), sessionCookie)
	if !cookie.HttpOnly {
		t.Error("session cookie is readable by scripts")
	}
	if !cookie.Secure {
		t.Error("session cookie is not Secure on an https request — it would travel in the clear")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("session cookie SameSite = %v, want Lax", cookie.SameSite)
	}
	if cookie.Path != "/" {
		t.Errorf("session cookie Path = %q, want %q — it is read on every route", cookie.Path, "/")
	}
	// Told to the browser as well as sealed into the payload, so a cookie
	// that can no longer be used also stops being sent.
	if cookie.MaxAge <= 0 || time.Duration(cookie.MaxAge)*time.Second > SessionTTL {
		t.Errorf("session cookie Max-Age = %ds, want positive and no longer than %s",
			cookie.MaxAge, SessionTTL)
	}
	if strings.Contains(cookie.Value, "owner@example.com") {
		t.Errorf("session cookie value is unencoded: %q", cookie.Value)
	}
}

// TestSetSession_MaxAgeAgreesWithTheExpiry: the cookie's Max-Age and the
// expiry sealed inside it are two statements about the same moment, and
// they have to be made by the same clock. Measuring one with time.Now()
// and the other with the Authenticator's clock puts a browser-side
// lifetime on the cookie that disagrees with the server-side one — the
// cookie stops being sent early, or lingers after it stops verifying.
func TestSetSession_MaxAgeAgreesWithTheExpiry(t *testing.T) {
	t.Parallel()

	a := newFakeGoogle(t).authenticator(t, nil)

	// A clock that is not the wall clock, which is the only way to tell the
	// two apart.
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }

	rec := httptest.NewRecorder()
	err := a.setSession(rec, httptest.NewRequest(http.MethodGet, "/", nil), Session{
		Email:     "owner@example.com",
		ExpiresAt: a.now().Add(SessionTTL),
	})
	if err != nil {
		t.Fatalf("setSession() error = %v", err)
	}

	cookie := findCookie(t, rec.Result().Cookies(), sessionCookie)
	if want := int(SessionTTL.Seconds()); cookie.MaxAge != want {
		t.Errorf("session cookie Max-Age = %ds, want %ds — measured against a different clock than the expiry it seals",
			cookie.MaxAge, want)
	}
}

// TestNew_RejectsAWeakKey: HMAC-SHA256 is worth exactly what its key is
// worth, and a passphrase somebody typed is not worth 256 bits. The app
// refuses to start rather than sign sessions with one — which is the same
// stance internal/config takes, and is checked in both places because
// either one alone could be bypassed by a future caller.
func TestNew_RejectsAWeakKey(t *testing.T) {
	t.Parallel()

	_, err := New(t.Context(), Config{
		ClientID:     testClientID,
		ClientSecret: testClientSecret,
		SessionKey:   []byte("hunter2"),
	})
	if err == nil {
		t.Fatal("New() error = nil, want a refusal for a 7-byte session key")
	}
}

// TestNew_RequiresCredentials: an Authenticator with no client id or
// secret cannot complete a sign-in, and the place to find that out is
// startup, not the login page.
func TestNew_RequiresCredentials(t *testing.T) {
	t.Parallel()

	if _, err := New(t.Context(), Config{SessionKey: testSessionKey()}); err == nil {
		t.Fatal("New() error = nil, want a refusal for missing OAuth credentials")
	}
}

// sessionPayload is the encoded payload half of a cookie value, without a
// signature — the raw material of a forgery.
func sessionPayload(t *testing.T, s Session) string {
	t.Helper()

	value := sessionOf(t, s)
	payload, _, _ := strings.Cut(value, ".")
	return payload
}
