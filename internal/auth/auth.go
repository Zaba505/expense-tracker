package auth

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// The routes this package serves. They are constants because the
// redirect URI is built from CallbackPath and must equal, byte for byte,
// one of the URIs registered on the OAuth client in the Google Cloud
// console — Google rejects the flow outright otherwise. Router, redirect
// URI, and sign-in link therefore all read it from here.
const (
	LoginPath    = "/auth/login"
	CallbackPath = "/auth/callback"
	LogoutPath   = "/logout"
)

// googleIssuer is the "iss" every Google ID token carries, and what the
// verifier holds them to.
const googleIssuer = "https://accounts.google.com"

// googleProvider is Google's OIDC configuration, written down rather than
// discovered.
//
// The alternative — oidc.NewProvider, which GETs Google's discovery
// document — puts a network call in the app's startup path, so a hiccup at
// accounts.google.com becomes a container that will not boot and a
// revision that will not roll out. This app already takes the opposite
// position with Firestore (connect lazily, boot anyway, report unready),
// and these three endpoints have been stable for the life of the API and
// are published by Google as such. The one thing discovery would buy —
// noticing a key-set URL move — the key set already handles: go-oidc
// re-fetches JWKS on an unknown key id.
var googleProvider = oidc.ProviderConfig{
	IssuerURL:   googleIssuer,
	AuthURL:     "https://accounts.google.com/o/oauth2/v2/auth",
	TokenURL:    "https://oauth2.googleapis.com/token",
	JWKSURL:     "https://www.googleapis.com/oauth2/v3/certs",
	UserInfoURL: "https://openidconnect.googleapis.com/v1/userinfo",
}

// Config is what an Authenticator needs to exist. Everything in it that is
// required comes from internal/config, which has already refused to boot
// without it.
type Config struct {
	// ClientID and ClientSecret identify this app to Google. The secret is
	// what lets it redeem an authorization code, which is why it lives in
	// Secret Manager and not in the environment of a laptop.
	ClientID     string
	ClientSecret string

	// SessionKey signs the cookies. At least MinSessionKeyLen bytes.
	SessionKey []byte

	// BaseURL is the app's public origin ("https://host"), used to build
	// the redirect URI. Empty means "take it from the request", which is
	// what makes the app work unchanged on localhost and on its Cloud Run
	// URL without either being written down.
	BaseURL string

	// Logger receives the reason a sign-in failed. The browser does not:
	// see fail.
	Logger *slog.Logger

	// provider and httpClient exist for tests, which stand up a fake
	// issuer — an httptest server with a JWKS and a token endpoint — and
	// point the Authenticator at it. Production has exactly one issuer and
	// no reason to name it, so neither is exported.
	provider   *oidc.ProviderConfig
	httpClient *http.Client

	// now is the clock, overridable so a test can age a cookie out without
	// sleeping.
	now func() time.Time
}

// Authenticator serves the login and callback routes and reads the
// sessions they hand out.
type Authenticator struct {
	clientID     string
	clientSecret string
	baseURL      string
	codec        codec
	logger       *slog.Logger
	verifier     *oidc.IDTokenVerifier
	endpoint     oauth2.Endpoint
	httpClient   *http.Client
	now          func() time.Time
}

// New builds an Authenticator. It makes no network call — ctx is here
// because the key set it constructs will use ctx's HTTP client when it
// first fetches Google's signing keys, which happens on the first sign-in
// and not before.
func New(ctx context.Context, cfg Config) (*Authenticator, error) {
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, fmt.Errorf("auth: client id and secret are required")
	}
	if len(cfg.SessionKey) < MinSessionKeyLen {
		return nil, fmt.Errorf("auth: session key is %d bytes, want at least %d",
			len(cfg.SessionKey), MinSessionKeyLen)
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	now := cfg.now
	if now == nil {
		now = time.Now
	}
	httpClient := cfg.httpClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	providerConfig := googleProvider
	if cfg.provider != nil {
		providerConfig = *cfg.provider
	}

	// The client goes into the context rather than into a field of the
	// key set, because that is the only way go-oidc and oauth2 both take
	// one. The provider it builds holds onto this context's client for
	// the JWKS fetches it makes later.
	provider := providerConfig.NewProvider(oidc.ClientContext(ctx, httpClient))

	return &Authenticator{
		clientID:     cfg.ClientID,
		clientSecret: cfg.ClientSecret,
		baseURL:      strings.TrimSuffix(cfg.BaseURL, "/"),
		codec:        codec{key: cfg.SessionKey},
		logger:       logger,
		// The verifier checks the ID token's signature against Google's
		// key set, that it was issued by Google, that it was issued *to
		// this app* (aud == our client id, which is what stops a token
		// minted for some other app being replayed here), and that it has
		// not expired.
		verifier:   provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		endpoint:   provider.Endpoint(),
		httpClient: httpClient,
		now:        now,
	}, nil
}

// LoginHandler starts a sign-in: it mints a state and a nonce, remembers
// them in a signed cookie, and sends the browser to Google.
//
// It does not care whether the caller already has a session. Re-running
// the flow is how you switch accounts, and refusing it would make a stale
// or half-formed session impossible to replace without clearing cookies by
// hand.
func (a *Authenticator) LoginHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flow := loginFlow{
			State:     randomToken(),
			Nonce:     randomToken(),
			ExpiresAt: a.now().Add(flowTTL),
		}

		if err := a.setLoginFlow(w, r, flow); err != nil {
			a.fail(w, r, http.StatusInternalServerError, "start login", err)
			return
		}

		// StatusFound, not 303: this is a GET redirecting to a GET, and
		// 302 is what every OAuth client on the internet sends here.
		http.Redirect(w, r, a.oauth2Config(r).AuthCodeURL(flow.State, oidc.Nonce(flow.Nonce)),
			http.StatusFound)
	})
}

// CallbackHandler finishes a sign-in: it checks that the callback belongs
// to a flow this app started, redeems the code, verifies the ID token
// Google returns, and — only then — hands the browser a session.
//
// Every check below is load-bearing. Skipping the state check makes the
// app forgeable into signing the owner into an attacker's account;
// skipping the nonce check makes an old ID token replayable; skipping
// verification makes the ID token a string the browser chose.
func (a *Authenticator) CallbackHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		query := r.URL.Query()

		// Google reports a refusal — the user cancelled, the client is
		// misconfigured — in the query string, with a 200. It is not our
		// error, but it is the end of this flow.
		if reason := query.Get("error"); reason != "" {
			a.clearLoginFlow(w, r)
			a.fail(w, r, http.StatusBadRequest, "google declined the sign-in",
				fmt.Errorf("%s: %s", reason, query.Get("error_description")))
			return
		}

		flow, err := a.loginFlow(r)
		if err != nil {
			a.fail(w, r, http.StatusBadRequest, "no login is in progress", err)
			return
		}

		// Spent as soon as it is read, whatever happens next: a state and
		// nonce are good for one attempt, and an attempt that failed is
		// still an attempt.
		a.clearLoginFlow(w, r)

		// Constant time, for the same reason the MAC comparison is: a
		// state compared with == leaks how many bytes of a guess were
		// right.
		if subtle.ConstantTimeCompare([]byte(query.Get("state")), []byte(flow.State)) != 1 {
			a.fail(w, r, http.StatusBadRequest, "this callback does not belong to your login",
				fmt.Errorf("state parameter does not match the state we issued"))
			return
		}

		code := query.Get("code")
		if code == "" {
			a.fail(w, r, http.StatusBadRequest, "the callback carried no authorization code",
				fmt.Errorf("no code parameter"))
			return
		}

		// From here on a failure is Google's or the network's, not the
		// browser's: the request was well-formed and came from a flow we
		// started. Hence 502 rather than 400 — the caller did nothing
		// wrong and retrying is the right advice.
		token, err := a.oauth2Config(r).Exchange(oidc.ClientContext(ctx, a.httpClient), code)
		if err != nil {
			a.fail(w, r, http.StatusBadGateway, "could not reach Google to complete the sign-in",
				fmt.Errorf("exchange authorization code: %w", err))
			return
		}

		// The access token is not the point and is deliberately dropped;
		// this app calls no Google API on the user's behalf. The ID token
		// riding alongside it is the whole payload — a signed statement of
		// who signed in.
		rawIDToken, ok := token.Extra("id_token").(string)
		if !ok {
			a.fail(w, r, http.StatusBadGateway, "google's response carried no identity",
				fmt.Errorf("token response has no id_token — was the openid scope requested?"))
			return
		}

		idToken, err := a.verifier.Verify(oidc.ClientContext(ctx, a.httpClient), rawIDToken)
		if err != nil {
			a.fail(w, r, http.StatusBadGateway, "google's identity token did not check out",
				fmt.Errorf("verify id token: %w", err))
			return
		}

		// The nonce came back inside a token only Google could have
		// signed, so this equality is the proof that the token was minted
		// for *this* login and is not one captured from an earlier one.
		if subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(flow.Nonce)) != 1 {
			a.fail(w, r, http.StatusBadRequest, "this identity token does not belong to your login",
				fmt.Errorf("id token nonce does not match the nonce we issued"))
			return
		}

		var claims struct {
			Email         string `json:"email"`
			EmailVerified bool   `json:"email_verified"`
		}
		if err := idToken.Claims(&claims); err != nil {
			a.fail(w, r, http.StatusBadGateway, "google's identity token could not be read",
				fmt.Errorf("decode id token claims: %w", err))
			return
		}

		// An unverified email is a string the account holder typed, not an
		// address they proved they own — and the allowlist (#14) is an
		// email comparison. Accepting one would let anybody who can name
		// the owner's address claim to be them.
		if claims.Email == "" || !claims.EmailVerified {
			a.fail(w, r, http.StatusForbidden, "your Google account has no verified email address",
				fmt.Errorf("email %q, verified %t", claims.Email, claims.EmailVerified))
			return
		}

		session := Session{
			Email:     claims.Email,
			Subject:   idToken.Subject,
			ExpiresAt: a.now().Add(SessionTTL),
		}
		if err := a.setSession(w, r, session); err != nil {
			a.fail(w, r, http.StatusInternalServerError, "could not start your session", err)
			return
		}

		a.logger.InfoContext(ctx, "signed in",
			slog.String("email", session.Email),
			slog.String("subject", session.Subject),
		)

		// SeeOther: the callback URL carries a spent authorization code,
		// and a 303 is what stops it sitting in the address bar to be
		// reloaded — the browser follows with a fresh GET of "/".
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})
}

// LogoutHandler ends the browser's session by clearing the session cookie and
// sending it back to the start of the sign-in flow.
func (a *Authenticator) LogoutHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.clearSession(w, r)
		http.Redirect(w, r, LoginPath, http.StatusSeeOther)
	})
}

// oauth2Config is the client credentials plus the redirect URI, which
// depends on the request when BaseURL is not pinned — hence per-request
// rather than a field.
func (a *Authenticator) oauth2Config(r *http.Request) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     a.clientID,
		ClientSecret: a.clientSecret,
		Endpoint:     a.endpoint,
		RedirectURL:  a.baseURLFor(r) + CallbackPath,
		// openid gets an ID token at all; email puts the address in it.
		// Nothing here needs a name or a picture, so nothing asks for one:
		// an unused scope is data the app is trusted with for no reason.
		Scopes: []string{oidc.ScopeOpenID, "email"},
	}
}

// baseURLFor is the app's public origin as this request saw it.
//
// Deriving it from the request is what lets the same binary serve a
// laptop's http://localhost:8080 and a Cloud Run URL nobody could have
// written into the config before the service existed (the URL is an output
// of creating it). BASE_URL overrides, for a deployment behind a custom
// domain where the request's own host is not the one Google was told
// about.
//
// A spoofed Host header cannot turn this into a redirect at an attacker's
// site: Google only ever redirects to a URI registered on the OAuth
// client, and refuses the flow otherwise. The worst a forged Host achieves
// is a sign-in that Google rejects.
func (a *Authenticator) baseURLFor(r *http.Request) string {
	if a.baseURL != "" {
		return a.baseURL
	}

	scheme := "http"
	if isHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// isHTTPS reports whether the browser reached the app over TLS.
//
// On Cloud Run it never reached *this process* over TLS — TLS terminates
// at the front end, which forwards plain HTTP and says so in
// X-Forwarded-Proto. The header is therefore load-bearing and safe to
// trust here: Cloud Run overwrites whatever a client sends, and this app
// is not deployed behind anything else. r.TLS covers the case of the
// server terminating TLS itself.
func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// fail ends a sign-in: the reason goes to the log, a plain sentence goes
// to the browser.
//
// The split is the point. These failures name the OAuth client, the
// issuer, and what did not match — an attacker probing the callback learns
// from any of it which of their guesses was closest, and the owner cannot
// act on it either.
func (a *Authenticator) fail(w http.ResponseWriter, r *http.Request, status int, public string, err error) {
	a.logger.ErrorContext(r.Context(), "sign-in failed",
		slog.String("path", r.URL.Path),
		slog.Int("status", status),
		slog.Any("error", err),
	)
	http.Error(w, "Sign-in failed: "+public+".", status)
}
