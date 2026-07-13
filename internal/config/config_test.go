package config

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/Zaba505/expense-tracker/internal/auth"
)

// testSessionKey is a valid SESSION_KEY: 32 bytes, base64-encoded, as
// `openssl rand -base64 32` would produce.
var testSessionKey = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("k"), auth.MinSessionKeyLen))

// mapEnv returns a getenv func backed by m, so tests never touch the
// real process environment (keeping them parallel-safe).
func mapEnv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// validEnv is an environment Load accepts, with overrides applied on top.
// Tests about one variable start from a valid whole and change only that
// variable — including to "", which mapEnv cannot tell from unset, and
// which is how the missing-value cases below are written.
//
// It exists so that the next required variable is one line here rather
// than an edit to every test in the file.
func validEnv(overrides map[string]string) map[string]string {
	env := map[string]string{
		"GCP_PROJECT":         "my-project",
		"OWNER_EMAIL":         "owner@example.com",
		"OAUTH_CLIENT_ID":     "client-id.apps.googleusercontent.com",
		"OAUTH_CLIENT_SECRET": "client-secret",
		"SESSION_KEY":         testSessionKey,
	}
	for k, v := range overrides {
		env[k] = v
	}
	return env
}

func TestLoad_MinimalRequired(t *testing.T) {
	t.Parallel()

	cfg, err := Load(mapEnv(validEnv(nil)))
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.Port != DefaultPort {
		t.Errorf("Port = %q, want default %q", cfg.Port, DefaultPort)
	}
	if cfg.GCPProject != "my-project" {
		t.Errorf("GCPProject = %q, want %q", cfg.GCPProject, "my-project")
	}
	if cfg.OwnerEmail != "owner@example.com" {
		t.Errorf("OwnerEmail = %q, want %q", cfg.OwnerEmail, "owner@example.com")
	}
	if cfg.OAuthClientID != "client-id.apps.googleusercontent.com" {
		t.Errorf("OAuthClientID = %q, want the id from the environment", cfg.OAuthClientID)
	}
	if cfg.OAuthClientSecret != "client-secret" {
		t.Errorf("OAuthClientSecret = %q, want the secret from the environment", cfg.OAuthClientSecret)
	}
	if len(cfg.SessionKey) != auth.MinSessionKeyLen {
		t.Errorf("SessionKey is %d bytes, want the %d decoded from SESSION_KEY",
			len(cfg.SessionKey), auth.MinSessionKeyLen)
	}
	if cfg.BaseURL != "" {
		t.Errorf("BaseURL = %q, want empty when BASE_URL is unset", cfg.BaseURL)
	}
	if cfg.UsesEmulator() {
		t.Errorf("UsesEmulator() = true, want false when FIRESTORE_EMULATOR_HOST unset")
	}
	if cfg.Addr() != ":"+DefaultPort {
		t.Errorf("Addr() = %q, want %q", cfg.Addr(), ":"+DefaultPort)
	}
}

func TestLoad_AllValues(t *testing.T) {
	t.Parallel()

	cfg, err := Load(mapEnv(validEnv(map[string]string{
		"PORT":                    "9090",
		"FIRESTORE_EMULATOR_HOST": "localhost:8181",
		"BASE_URL":                "https://expenses.example.com",
	})))
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.Port != "9090" {
		t.Errorf("Port = %q, want %q", cfg.Port, "9090")
	}
	if cfg.FirestoreEmulatorHost != "localhost:8181" {
		t.Errorf("FirestoreEmulatorHost = %q, want %q", cfg.FirestoreEmulatorHost, "localhost:8181")
	}
	if cfg.BaseURL != "https://expenses.example.com" {
		t.Errorf("BaseURL = %q, want %q", cfg.BaseURL, "https://expenses.example.com")
	}
	if !cfg.UsesEmulator() {
		t.Errorf("UsesEmulator() = false, want true when emulator host set")
	}
	if cfg.Addr() != ":9090" {
		t.Errorf("Addr() = %q, want %q", cfg.Addr(), ":9090")
	}
}

func TestLoad_TrimsWhitespace(t *testing.T) {
	t.Parallel()

	cfg, err := Load(mapEnv(validEnv(map[string]string{
		"PORT":                "  8080  ",
		"GCP_PROJECT":         "  proj  ",
		"OWNER_EMAIL":         "\towner@example.com\n",
		"OAUTH_CLIENT_ID":     "  client-id  ",
		"OAUTH_CLIENT_SECRET": "\tclient-secret\n",
		"SESSION_KEY":         "  " + testSessionKey + "\n",
	})))
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.Port != "8080" {
		t.Errorf("Port = %q, want trimmed %q", cfg.Port, "8080")
	}
	if cfg.GCPProject != "proj" {
		t.Errorf("GCPProject = %q, want trimmed %q", cfg.GCPProject, "proj")
	}
	if cfg.OwnerEmail != "owner@example.com" {
		t.Errorf("OwnerEmail = %q, want trimmed %q", cfg.OwnerEmail, "owner@example.com")
	}
	if cfg.OAuthClientID != "client-id" {
		t.Errorf("OAuthClientID = %q, want trimmed %q", cfg.OAuthClientID, "client-id")
	}
	if cfg.OAuthClientSecret != "client-secret" {
		t.Errorf("OAuthClientSecret = %q, want trimmed %q", cfg.OAuthClientSecret, "client-secret")
	}
	// A key with a stray newline is the likeliest way to hand this
	// variable a value, since that is what `openssl rand -base64 32 |
	// pbcopy` and a here-doc both produce — and base64 will not decode it.
	if len(cfg.SessionKey) != auth.MinSessionKeyLen {
		t.Errorf("SessionKey is %d bytes, want a SESSION_KEY with surrounding whitespace to decode",
			len(cfg.SessionKey))
	}
}

// TestLoad_WhitespaceOnlyRequiredIsMissing ensures a value that is only
// whitespace is treated as unset, not as a valid value.
func TestLoad_WhitespaceOnlyRequiredIsMissing(t *testing.T) {
	t.Parallel()

	_, err := Load(mapEnv(validEnv(map[string]string{"GCP_PROJECT": "   "})))
	if err == nil {
		t.Fatal("Load() error = nil, want error for whitespace-only GCP_PROJECT")
	}
	if !strings.Contains(err.Error(), "GCP_PROJECT") {
		t.Errorf("error = %q, want mention of GCP_PROJECT", err)
	}
}

func TestLoad_MissingRequired(t *testing.T) {
	t.Parallel()

	// Every required variable, one at a time: unset it, and Load must name
	// it and only it.
	required := []string{
		"GCP_PROJECT",
		"OWNER_EMAIL",
		"OAUTH_CLIENT_ID",
		"OAUTH_CLIENT_SECRET",
		"SESSION_KEY",
	}

	for _, name := range required {
		t.Run("missing "+name, func(t *testing.T) {
			t.Parallel()

			cfg, err := Load(mapEnv(validEnv(map[string]string{name: ""})))
			if err == nil {
				t.Fatalf("Load() without %s: error = nil, want error", name)
			}
			if cfg != nil {
				t.Errorf("Load() cfg = %+v, want nil on error", cfg)
			}
			if !strings.Contains(err.Error(), name) {
				t.Errorf("error %q does not name the missing variable %q", err, name)
			}
			for _, other := range required {
				if other != name && strings.Contains(err.Error(), other) {
					t.Errorf("error %q blames %q, but only %q was missing", err, other, name)
				}
			}
		})
	}
}

func TestLoad_InvalidPort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		port string
	}{
		{"not a number", "abc"},
		{"zero", "0"},
		{"negative", "-1"},
		{"too large", "70000"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := Load(mapEnv(validEnv(map[string]string{"PORT": tt.port})))
			if err == nil {
				t.Fatalf("Load() error = nil, want error for PORT=%q", tt.port)
			}
			if !strings.Contains(err.Error(), "PORT") {
				t.Errorf("error = %q, want mention of PORT", err)
			}
		})
	}
}

// TestLoad_InvalidSessionKey covers the two ways a signing key can be
// present and still useless. The short-key case is the one that matters:
// it is a plausible thing to type ("secret" as a passphrase), it looks
// like it works, and it silently weakens every session cookie the app
// signs.
func TestLoad_InvalidSessionKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		key  string
	}{
		{"not base64", "not base64!!"},
		{"too short", base64.StdEncoding.EncodeToString([]byte("16-bytes-of-key."))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := Load(mapEnv(validEnv(map[string]string{"SESSION_KEY": tt.key})))
			if err == nil {
				t.Fatalf("Load() error = nil, want error for SESSION_KEY=%q", tt.key)
			}
			if !strings.Contains(err.Error(), "SESSION_KEY") {
				t.Errorf("error = %q, want mention of SESSION_KEY", err)
			}
			// The key is a secret and this error is on its way to a log.
			if strings.Contains(err.Error(), tt.key) {
				t.Errorf("error %q quotes the session key back", err)
			}
		})
	}
}

func TestLoad_BaseURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		baseURL string
		want    string // empty means Load must reject it
	}{
		{name: "https origin", baseURL: "https://expenses.example.com", want: "https://expenses.example.com"},
		{name: "http localhost", baseURL: "http://localhost:8080", want: "http://localhost:8080"},
		// A trailing slash would build "//auth/callback", which is not the
		// redirect URI registered with Google — so it is trimmed, not
		// rejected: it is a typo, not a misconfiguration.
		{name: "trailing slash is trimmed", baseURL: "https://expenses.example.com/", want: "https://expenses.example.com"},
		{name: "no scheme", baseURL: "expenses.example.com"},
		{name: "not http", baseURL: "ftp://expenses.example.com"},
		{name: "no host", baseURL: "https://"},
		{name: "carries a path", baseURL: "https://expenses.example.com/app"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := Load(mapEnv(validEnv(map[string]string{"BASE_URL": tt.baseURL})))

			if tt.want == "" {
				if err == nil {
					t.Fatalf("Load() error = nil, want error for BASE_URL=%q", tt.baseURL)
				}
				if !strings.Contains(err.Error(), "BASE_URL") {
					t.Errorf("error = %q, want mention of BASE_URL", err)
				}
				return
			}

			if err != nil {
				t.Fatalf("Load() error = %v, want nil for BASE_URL=%q", err, tt.baseURL)
			}
			if cfg.BaseURL != tt.want {
				t.Errorf("BaseURL = %q, want %q", cfg.BaseURL, tt.want)
			}
		})
	}
}

// TestLoad_ReportsAllProblemsAtOnce verifies the fail-fast error is
// aggregate: a config broken in several ways names every problem so it
// can be fixed in a single pass.
func TestLoad_ReportsAllProblemsAtOnce(t *testing.T) {
	t.Parallel()

	_, err := Load(mapEnv(map[string]string{
		"PORT": "not-a-port",
		// everything required is missing
	}))
	if err == nil {
		t.Fatal("Load() error = nil, want aggregate error")
	}
	for _, want := range []string{
		"PORT", "GCP_PROJECT", "OWNER_EMAIL", "OAUTH_CLIENT_ID", "OAUTH_CLIENT_SECRET", "SESSION_KEY",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("aggregate error %q missing %q", err, want)
		}
	}
}
