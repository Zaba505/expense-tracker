package auth

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// fakeGoogle is an OIDC issuer under the test's control: a JWKS endpoint
// and a token endpoint, with a real RSA key behind them.
//
// It exists so the callback handler can be tested through its actual
// verification path — a genuine RS256 signature, checked against a genuine
// key set — rather than with the verifier stubbed out. Every attack the
// handler is supposed to stop (a forged token, one signed by the wrong
// key, one minted for another app, one replayed from an earlier login) is
// a token this fake can be told to issue, and the difference between
// catching it and not is a real signature check.
type fakeGoogle struct {
	server *httptest.Server
	key    *rsa.PrivateKey

	// mu guards the fields below, which a test sets before the request and
	// reads after it, while the server's handler touches them in between.
	mu sync.Mutex

	// claims is the ID token's payload. Tests mutate it to forge one.
	claims map[string]any

	// signWith overrides the signing key — an attacker's key, in the test
	// that proves an unverifiable token is refused.
	signWith *rsa.PrivateKey

	// omitIDToken drops the id_token from the token response, which is
	// what a misconfigured scope looks like from here.
	omitIDToken bool

	// tokenStatus is the token endpoint's status code; anything but 200 is
	// a Google that would not talk to us.
	tokenStatus int

	// tokenForm records what the app actually posted to redeem the code.
	tokenForm url.Values
}

// testKey is the fake issuer's signing key, and attackerKey is a valid RSA
// key that is not it — the difference between a token the verifier must
// accept and one it must refuse.
//
// Both are generated once for the whole package: RSA key generation is slow
// enough to be felt across a table of tests, and nothing here needs a fresh
// key per test.
var (
	testKey     = sync.OnceValue(generateKey)
	attackerKey = sync.OnceValue(generateKey)
)

func generateKey() *rsa.PrivateKey {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return key
}

const testKeyID = "test-key"

// newFakeGoogle stands up the issuer and returns it, closed at test end.
func newFakeGoogle(t *testing.T) *fakeGoogle {
	t.Helper()

	g := &fakeGoogle{
		key:         testKey(),
		tokenStatus: http.StatusOK,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /certs", g.serveJWKS)
	mux.HandleFunc("POST /token", g.serveToken)

	g.server = httptest.NewServer(mux)
	t.Cleanup(g.server.Close)

	// The default token is the one a real, correct sign-in produces:
	// issued by this fake, for this client, for a verified owner, valid
	// now. Each failure test spoils exactly one of those.
	g.claims = map[string]any{
		"iss":            g.server.URL,
		"aud":            testClientID,
		"sub":            "google-subject-id",
		"email":          "owner@example.com",
		"email_verified": true,
		"iat":            time.Now().Unix(),
		"exp":            time.Now().Add(time.Hour).Unix(),
	}
	return g
}

// authenticator is an Authenticator pointed at this fake instead of at
// Google. now, if non-nil, is its clock.
func (g *fakeGoogle) authenticator(t *testing.T, now func() time.Time) *Authenticator {
	t.Helper()

	a, err := New(t.Context(), Config{
		ClientID:     testClientID,
		ClientSecret: testClientSecret,
		SessionKey:   testSessionKey(),
		provider: &oidc.ProviderConfig{
			IssuerURL: g.server.URL,
			AuthURL:   g.server.URL + "/authorize",
			TokenURL:  g.server.URL + "/token",
			JWKSURL:   g.server.URL + "/certs",
		},
		httpClient: g.server.Client(),
		now:        now,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return a
}

// setClaim spoils (or sets) one claim of the ID token to come.
func (g *fakeGoogle) setClaim(name string, value any) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.claims[name] = value
}

// serveJWKS publishes the public half of the signing key, which is how the
// verifier learns to check the signature.
func (g *fakeGoogle) serveJWKS(w http.ResponseWriter, r *http.Request) {
	pub := g.key.Public().(*rsa.PublicKey)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"keys": []map[string]any{{
			"kty": "RSA",
			"alg": "RS256",
			"use": "sig",
			"kid": testKeyID,
			"n":   b64(pub.N.Bytes()),
			"e":   b64(big.NewInt(int64(pub.E)).Bytes()),
		}},
	})
}

// serveToken is the token endpoint: it records what was posted and hands
// back whatever the test has arranged.
func (g *fakeGoogle) serveToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	g.tokenForm = r.PostForm

	if g.tokenStatus != http.StatusOK {
		http.Error(w, `{"error":"invalid_grant"}`, g.tokenStatus)
		return
	}

	body := map[string]any{
		"access_token": "an-access-token-this-app-has-no-use-for",
		"token_type":   "Bearer",
		"expires_in":   3600,
	}
	if !g.omitIDToken {
		key := g.signWith
		if key == nil {
			key = g.key
		}
		body["id_token"] = signJWT(key, g.claims)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

// signJWT produces an RS256 JWT — a real one, with a real signature, so
// that the verifier under test does real work.
func signJWT(key *rsa.PrivateKey, claims map[string]any) string {
	header, err := json.Marshal(map[string]any{
		"alg": "RS256",
		"typ": "JWT",
		"kid": testKeyID,
	})
	if err != nil {
		panic(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		panic(err)
	}

	signing := b64(header) + "." + b64(payload)
	digest := sha256.Sum256([]byte(signing))

	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		panic(err)
	}
	return signing + "." + b64(signature)
}

// b64 is the base64url-without-padding every part of a JWT is encoded in.
func b64(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

// postedToTokenEndpoint returns the form the app sent to redeem the code.
func (g *fakeGoogle) postedToTokenEndpoint() url.Values {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.tokenForm
}

// String makes a failure message name the issuer it was talking to.
func (g *fakeGoogle) String() string {
	return fmt.Sprintf("fake google at %s", g.server.URL)
}
