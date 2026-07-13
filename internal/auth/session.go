package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// SessionTTL is how long a session cookie is good for. A stateless cookie
// cannot be revoked (see the package doc), so this is the only bound on a
// stolen one — a day, which is short enough to matter and long enough that
// the owner is not signing in between one look at last month and the next.
const SessionTTL = 24 * time.Hour

// flowTTL bounds the login attempt itself: the window between being sent
// to Google and coming back. It is the lifetime of the state and nonce, so
// it is also how long a captured authorization code has to be replayed in.
// Ten minutes is a slow human, not a slow attack.
const flowTTL = 10 * time.Minute

// MinSessionKeyLen is the shortest signing key accepted. HMAC-SHA256's
// security rests entirely on this key, and 256 bits is the size of its
// output: a shorter key is the weakest link, a longer one buys nothing.
const MinSessionKeyLen = 32

// Cookie names. Prefixed so they cannot collide with anything else served
// from this origin.
const (
	sessionCookie = "et_session"
	flowCookie    = "et_login"
)

// Session is who the browser is. It is the decoded contents of the session
// cookie — everything the app knows about the caller, and nothing it would
// have to look up.
type Session struct {
	// Email is the Google account's verified email address. It is the
	// identity the app cares about: the owner allowlist compares
	// against it.
	Email string `json:"email"`

	// Subject is Google's stable, immutable id for the account ("sub").
	// Kept because an email can be reassigned and this cannot, so it is
	// the honest thing to log.
	Subject string `json:"sub"`

	// ExpiresAt is when this session stops being one. Carried inside the
	// signed payload rather than left to the cookie's own Max-Age, because
	// Max-Age is enforced by the browser and the browser is the party we
	// are defending against: a client that simply keeps sending an expired
	// cookie must still be told no.
	ExpiresAt time.Time `json:"exp"`
}

// expired reports whether the session is past its expiry as of now.
func (s Session) expired(now time.Time) bool {
	return !now.Before(s.ExpiresAt)
}

// loginFlow is the state carried across the redirect to Google, in a
// cookie of its own. It exists because the app keeps no server-side
// session store: the values minted at /auth/login have to survive until
// /auth/callback somehow, and the browser is the only thing that makes the
// round trip.
//
// Putting them in a signed cookie is what makes them trustworthy on the
// way back — an attacker can drop this cookie, but cannot forge one that
// says a state they chose is the state we minted.
type loginFlow struct {
	// State is echoed by Google in the query string and compared with this
	// copy. It is what makes the callback un-forgeable: without it, an
	// attacker could feed the owner's browser a callback URL carrying the
	// attacker's own authorization code and log the owner into the
	// attacker's account (session fixation).
	State string `json:"state"`

	// Nonce is echoed by Google *inside the ID token*, where only Google
	// can put it. State proves the callback belongs to a flow this app
	// started; nonce proves the ID token was minted for that same flow and
	// is not an older one replayed at us.
	Nonce string `json:"nonce"`

	ExpiresAt time.Time `json:"exp"`
}

func (f loginFlow) expired(now time.Time) bool {
	return !now.Before(f.ExpiresAt)
}

// codec seals a payload into a cookie value and opens it again, proving it
// was not touched in between. The payload is signed, not encrypted: its
// contents (an email, an expiry) are already known to the owner's browser,
// and secrecy is not what a session cookie needs — integrity is.
type codec struct {
	key []byte
}

// seal returns "<payload>.<mac>", both base64url, where mac authenticates
// the encoded payload.
func (c codec) seal(v any) (string, error) {
	payload, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("encode cookie payload: %w", err)
	}

	encoded := base64.RawURLEncoding.EncodeToString(payload)
	return encoded + "." + base64.RawURLEncoding.EncodeToString(c.mac(encoded)), nil
}

// errBadCookie is what every way of failing to open a cookie collapses to.
// A caller cannot act on the difference between a truncated cookie, a
// forged one, and one signed with the previous key — all three mean "do
// not trust this" — and telling them apart in an error message tells an
// attacker which part of their guess was wrong.
var errBadCookie = errors.New("cookie is missing, malformed, or not authentic")

// open verifies the value's signature and decodes its payload into v.
func (c codec) open(value string, v any) error {
	encoded, mac, ok := strings.Cut(value, ".")
	if !ok {
		return errBadCookie
	}

	got, err := base64.RawURLEncoding.DecodeString(mac)
	if err != nil {
		return errBadCookie
	}

	// Constant time: a byte-by-byte comparison that returns early leaks,
	// through its timing, how much of a forged signature was right — which
	// is enough to build a valid one a byte at a time.
	if !hmac.Equal(got, c.mac(encoded)) {
		return errBadCookie
	}

	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return errBadCookie
	}

	// Only now, having proved the bytes are ours, do they get parsed. The
	// signature check is the gate; nothing upstream of it is trusted input.
	if err := json.Unmarshal(payload, v); err != nil {
		return errBadCookie
	}
	return nil
}

func (c codec) mac(encoded string) []byte {
	h := hmac.New(sha256.New, c.key)
	h.Write([]byte(encoded))
	return h.Sum(nil)
}

// setSession puts the session on the browser.
func (a *Authenticator) setSession(w http.ResponseWriter, r *http.Request, s Session) error {
	value, err := a.codec.seal(s)
	if err != nil {
		return err
	}

	http.SetCookie(w, a.cookie(r, &http.Cookie{
		Name:  sessionCookie,
		Value: value,
		Path:  "/",
		// The browser is told the same expiry the payload carries, so a
		// cookie that can no longer be used also stops being sent. Measured
		// against a.now() rather than time.Now(): the payload's expiry was
		// set by that clock, and two clocks would put a Max-Age on the
		// cookie that disagrees with the expiry inside it.
		MaxAge: int(s.ExpiresAt.Sub(a.now()).Seconds()),
	}))
	return nil
}

// Session returns the caller's session, and whether there is one. A
// cookie that is absent, malformed, unsigned, signed with a key that is
// not ours, or expired all answer the same way: no session.
//
// It is the read side of the login flow, and what the authorization
// middleware is built on.
func (a *Authenticator) Session(r *http.Request) (Session, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return Session{}, false
	}

	var s Session
	if err := a.codec.open(c.Value, &s); err != nil {
		return Session{}, false
	}
	if s.expired(a.now()) {
		return Session{}, false
	}
	return s, true
}

// ClearSession removes the session cookie from the browser.
func (a *Authenticator) ClearSession(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, a.cookie(r, &http.Cookie{
		Name:   sessionCookie,
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	}))
}

// setLoginFlow stashes the state and nonce for the trip to Google.
func (a *Authenticator) setLoginFlow(w http.ResponseWriter, r *http.Request, f loginFlow) error {
	value, err := a.codec.seal(f)
	if err != nil {
		return err
	}

	http.SetCookie(w, a.cookie(r, &http.Cookie{
		Name:  flowCookie,
		Value: value,
		// Scoped to the flow that uses it: this cookie is meaningless
		// outside /auth/callback, and a cookie that is not sent cannot be
		// stolen from a request that had no business carrying it.
		Path:   "/auth",
		MaxAge: int(flowTTL.Seconds()),
	}))
	return nil
}

// loginFlow reads back what setLoginFlow stashed.
func (a *Authenticator) loginFlow(r *http.Request) (loginFlow, error) {
	c, err := r.Cookie(flowCookie)
	if err != nil {
		return loginFlow{}, errBadCookie
	}

	var f loginFlow
	if err := a.codec.open(c.Value, &f); err != nil {
		return loginFlow{}, err
	}
	if f.expired(a.now()) {
		return loginFlow{}, fmt.Errorf("login attempt is older than %s", flowTTL)
	}
	return f, nil
}

// clearLoginFlow removes the flow cookie once it has done its job. It is
// single-use on purpose: leaving a spent state and nonce on the browser
// would let a captured callback URL be replayed while the cookie lived.
func (a *Authenticator) clearLoginFlow(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, a.cookie(r, &http.Cookie{
		Name:   flowCookie,
		Value:  "",
		Path:   "/auth",
		MaxAge: -1,
	}))
}

// cookie stamps the attributes every cookie this package sets must carry,
// so no call site can forget one.
func (a *Authenticator) cookie(r *http.Request, c *http.Cookie) *http.Cookie {
	// No script has any reason to read these, and the one that would want
	// to is not ours.
	c.HttpOnly = true

	// Lax, not Strict: the callback arrives as a top-level navigation from
	// accounts.google.com, and Strict would withhold the flow cookie from
	// exactly the request that needs it — the login would fail every time.
	// Lax sends cookies on a top-level GET, which is what this is, and
	// still withholds them from cross-site POSTs and embedded requests.
	c.SameSite = http.SameSiteLaxMode

	// Secure whenever the connection is https — always, in production.
	// Hardcoding it would make the app un-runnable over plain http on a
	// laptop, where the browser silently drops Secure cookies and the
	// symptom is a login that appears to do nothing.
	c.Secure = isHTTPS(r)

	return c
}

// randomToken is a fresh, unguessable value for a state or a nonce. 256
// bits from crypto/rand: the guarantee both values rest on is that an
// attacker cannot predict them.
func randomToken() string {
	var b [32]byte
	// The error is ignored because there is not one to have: as of Go 1.24
	// crypto/rand.Read is documented never to fail — it panics rather than
	// return a short read. So there is no path here where a token comes out
	// anything less than fully random, which is the property the state and
	// the nonce both rest on.
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}
