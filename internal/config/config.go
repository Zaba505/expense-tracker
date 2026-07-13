// Package config loads the expense-tracker's runtime configuration from
// the environment and fails fast when a required value is missing.
//
// Configuration is intentionally environment-driven so the same binary
// runs unchanged locally (against the Firestore emulator) and on Cloud
// Run (against native Firestore). Load reports every problem it finds at
// once, so a misconfigured deployment surfaces all missing values in a
// single startup error rather than one restart at a time.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/Zaba505/expense-tracker/internal/auth"
)

// DefaultPort is used when PORT is unset. Cloud Run always injects PORT,
// but local `go run ./cmd/server` typically does not, so a sensible
// default keeps the dev loop friction-free.
const DefaultPort = "8080"

// Config is the fully-validated runtime configuration. A non-nil *Config
// returned from Load is guaranteed to have every required field set.
type Config struct {
	// Port is the TCP port the HTTP server listens on. Defaults to
	// DefaultPort when PORT is unset. Always a valid 1–65535 port.
	Port string

	// GCPProject is the Google Cloud project id that owns the Firestore
	// database. Required.
	GCPProject string

	// FirestoreEmulatorHost, when set, points the Firestore client at a
	// local emulator (host:port) instead of the live service. Optional;
	// empty means "use native Firestore via ADC".
	FirestoreEmulatorHost string

	// OwnerEmail is the single Google account permitted to use the app;
	// it backs the auth allowlist. Required.
	OwnerEmail string

	// OAuthClientID and OAuthClientSecret are the app's Google Sign-In
	// credentials. Both are required: the app authenticates its callers,
	// so a deployment that cannot complete a sign-in is a deployment
	// nobody can use, and finding that out at startup beats finding it out
	// on the login page.
	//
	// The id is not a secret (it is in every authorization URL the browser
	// sees); the secret is, and comes from Secret Manager.
	OAuthClientID     string
	OAuthClientSecret string

	// SessionKey signs the session cookie. Required, and secret: it is the
	// only thing standing between an attacker and a cookie that says they
	// are the owner. Supplied base64-encoded because it is bytes, not
	// text, and an environment variable can only carry text:
	//
	//	openssl rand -base64 32
	SessionKey []byte

	// BaseURL pins the public origin ("https://expenses.example.com") the
	// OAuth redirect URI is built from. Optional: unset, the app takes the
	// origin from the request it is serving, which is what lets the same
	// binary run on localhost and on a Cloud Run URL that did not exist
	// when this config was written. Set it when the app is behind a custom
	// domain whose host is not the one reaching the container.
	BaseURL string
}

// UsesEmulator reports whether the config targets a local Firestore
// emulator rather than the live service.
func (c *Config) UsesEmulator() bool {
	return c.FirestoreEmulatorHost != ""
}

// Addr returns the ":PORT" listen address for net/http.
func (c *Config) Addr() string {
	return ":" + c.Port
}

// Load reads configuration using getenv (pass os.Getenv in production)
// and validates it. It returns an error naming every missing or invalid
// value at once so a misconfigured environment can be fixed in one pass.
//
// Required: GCP_PROJECT, OWNER_EMAIL, OAUTH_CLIENT_ID, OAUTH_CLIENT_SECRET,
// SESSION_KEY.
// Optional: PORT (defaults to DefaultPort), FIRESTORE_EMULATOR_HOST,
// BASE_URL.
func Load(getenv func(string) string) (*Config, error) {
	cfg := &Config{
		Port:                  strings.TrimSpace(getenv("PORT")),
		GCPProject:            strings.TrimSpace(getenv("GCP_PROJECT")),
		FirestoreEmulatorHost: strings.TrimSpace(getenv("FIRESTORE_EMULATOR_HOST")),
		OwnerEmail:            strings.TrimSpace(getenv("OWNER_EMAIL")),
		OAuthClientID:         strings.TrimSpace(getenv("OAUTH_CLIENT_ID")),
		OAuthClientSecret:     strings.TrimSpace(getenv("OAUTH_CLIENT_SECRET")),
		BaseURL:               strings.TrimSpace(getenv("BASE_URL")),
	}

	var errs []error

	if cfg.Port == "" {
		cfg.Port = DefaultPort
	} else if err := validatePort(cfg.Port); err != nil {
		errs = append(errs, err)
	}

	if cfg.GCPProject == "" {
		errs = append(errs, errMissing("GCP_PROJECT"))
	}
	if cfg.OwnerEmail == "" {
		errs = append(errs, errMissing("OWNER_EMAIL"))
	}
	if cfg.OAuthClientID == "" {
		errs = append(errs, errMissing("OAUTH_CLIENT_ID"))
	}
	if cfg.OAuthClientSecret == "" {
		errs = append(errs, errMissing("OAUTH_CLIENT_SECRET"))
	}

	key, err := sessionKey(strings.TrimSpace(getenv("SESSION_KEY")))
	if err != nil {
		errs = append(errs, err)
	}
	cfg.SessionKey = key

	if cfg.BaseURL != "" {
		if err := validateBaseURL(cfg.BaseURL); err != nil {
			errs = append(errs, err)
		}
		// A trailing slash would build "https://host//auth/callback",
		// which is a different redirect URI than the one registered with
		// Google — and Google's rejection of it names neither the slash
		// nor this variable.
		cfg.BaseURL = strings.TrimSuffix(cfg.BaseURL, "/")
	}

	if len(errs) > 0 {
		return nil, fmt.Errorf("config: %w", errors.Join(errs...))
	}
	return cfg, nil
}

// sessionKey decodes SESSION_KEY and insists it is long enough to be a
// key. The length is checked here rather than left to the auth package
// because this is where a bad environment is supposed to stop the process:
// a 4-byte key is not a configuration to run with and report on, it is a
// configuration to die on.
func sessionKey(encoded string) ([]byte, error) {
	if encoded == "" {
		return nil, errMissing("SESSION_KEY")
	}

	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		// The value itself never appears in the error — it is a signing
		// key, and this error is on its way to a log.
		return nil, fmt.Errorf("SESSION_KEY is not valid base64 (generate one with `openssl rand -base64 32`)")
	}
	if len(key) < auth.MinSessionKeyLen {
		return nil, fmt.Errorf("SESSION_KEY decodes to %d bytes, want at least %d", len(key), auth.MinSessionKeyLen)
	}
	return key, nil
}

// validateBaseURL insists on an absolute http(s) origin and nothing more —
// the app appends its own paths to it.
func validateBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("BASE_URL %q is not a valid URL", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("BASE_URL %q must be an absolute http:// or https:// URL", raw)
	}
	if u.Host == "" {
		return fmt.Errorf("BASE_URL %q has no host", raw)
	}
	if strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("BASE_URL %q must be an origin only, with no path, query, or fragment", raw)
	}
	return nil
}

// errMissing is the canonical error for an unset required variable.
func errMissing(name string) error {
	return fmt.Errorf("required environment variable %s is not set", name)
}

// validatePort ensures PORT is a base-10 integer in the valid TCP range.
func validatePort(port string) error {
	n, err := strconv.Atoi(port)
	if err != nil {
		return fmt.Errorf("PORT %q is not a valid integer", port)
	}
	if n < 1 || n > 65535 {
		return fmt.Errorf("PORT %d is out of range (1-65535)", n)
	}
	return nil
}
