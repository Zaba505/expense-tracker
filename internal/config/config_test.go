package config

import (
	"strings"
	"testing"
)

// mapEnv returns a getenv func backed by m, so tests never touch the
// real process environment (keeping them parallel-safe).
func mapEnv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoad_MinimalRequired(t *testing.T) {
	t.Parallel()

	cfg, err := Load(mapEnv(map[string]string{
		"GCP_PROJECT": "my-project",
		"OWNER_EMAIL": "owner@example.com",
	}))
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
	if cfg.UsesEmulator() {
		t.Errorf("UsesEmulator() = true, want false when FIRESTORE_EMULATOR_HOST unset")
	}
	if cfg.Addr() != ":"+DefaultPort {
		t.Errorf("Addr() = %q, want %q", cfg.Addr(), ":"+DefaultPort)
	}
}

func TestLoad_AllValues(t *testing.T) {
	t.Parallel()

	cfg, err := Load(mapEnv(map[string]string{
		"PORT":                    "9090",
		"GCP_PROJECT":             "proj",
		"FIRESTORE_EMULATOR_HOST": "localhost:8181",
		"OWNER_EMAIL":             "owner@example.com",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.Port != "9090" {
		t.Errorf("Port = %q, want %q", cfg.Port, "9090")
	}
	if cfg.FirestoreEmulatorHost != "localhost:8181" {
		t.Errorf("FirestoreEmulatorHost = %q, want %q", cfg.FirestoreEmulatorHost, "localhost:8181")
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

	cfg, err := Load(mapEnv(map[string]string{
		"PORT":        "  8080  ",
		"GCP_PROJECT": "  proj  ",
		"OWNER_EMAIL": "\towner@example.com\n",
	}))
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
}

// TestLoad_WhitespaceOnlyRequiredIsMissing ensures a value that is only
// whitespace is treated as unset, not as a valid value.
func TestLoad_WhitespaceOnlyRequiredIsMissing(t *testing.T) {
	t.Parallel()

	_, err := Load(mapEnv(map[string]string{
		"GCP_PROJECT": "   ",
		"OWNER_EMAIL": "owner@example.com",
	}))
	if err == nil {
		t.Fatal("Load() error = nil, want error for whitespace-only GCP_PROJECT")
	}
	if !strings.Contains(err.Error(), "GCP_PROJECT") {
		t.Errorf("error = %q, want mention of GCP_PROJECT", err)
	}
}

func TestLoad_MissingRequired(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		env     map[string]string
		wantIn  []string // substrings that must appear in the error
		wantOut []string // substrings that must NOT appear
	}{
		{
			name:    "missing both",
			env:     map[string]string{},
			wantIn:  []string{"GCP_PROJECT", "OWNER_EMAIL"},
			wantOut: nil,
		},
		{
			name:    "missing gcp project only",
			env:     map[string]string{"OWNER_EMAIL": "owner@example.com"},
			wantIn:  []string{"GCP_PROJECT"},
			wantOut: []string{"OWNER_EMAIL"},
		},
		{
			name:    "missing owner email only",
			env:     map[string]string{"GCP_PROJECT": "proj"},
			wantIn:  []string{"OWNER_EMAIL"},
			wantOut: []string{"GCP_PROJECT"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := Load(mapEnv(tt.env))
			if err == nil {
				t.Fatalf("Load() error = nil, want error")
			}
			if cfg != nil {
				t.Errorf("Load() cfg = %+v, want nil on error", cfg)
			}
			for _, s := range tt.wantIn {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("error %q missing expected substring %q", err, s)
				}
			}
			for _, s := range tt.wantOut {
				if strings.Contains(err.Error(), s) {
					t.Errorf("error %q unexpectedly mentions %q", err, s)
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
			_, err := Load(mapEnv(map[string]string{
				"PORT":        tt.port,
				"GCP_PROJECT": "proj",
				"OWNER_EMAIL": "owner@example.com",
			}))
			if err == nil {
				t.Fatalf("Load() error = nil, want error for PORT=%q", tt.port)
			}
			if !strings.Contains(err.Error(), "PORT") {
				t.Errorf("error = %q, want mention of PORT", err)
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
		// GCP_PROJECT and OWNER_EMAIL both missing
	}))
	if err == nil {
		t.Fatal("Load() error = nil, want aggregate error")
	}
	for _, want := range []string{"PORT", "GCP_PROJECT", "OWNER_EMAIL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("aggregate error %q missing %q", err, want)
		}
	}
}
