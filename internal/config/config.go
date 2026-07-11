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
	"errors"
	"fmt"
	"strconv"
	"strings"
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
// Required: GCP_PROJECT, OWNER_EMAIL.
// Optional: PORT (defaults to DefaultPort), FIRESTORE_EMULATOR_HOST.
func Load(getenv func(string) string) (*Config, error) {
	cfg := &Config{
		Port:                  strings.TrimSpace(getenv("PORT")),
		GCPProject:            strings.TrimSpace(getenv("GCP_PROJECT")),
		FirestoreEmulatorHost: strings.TrimSpace(getenv("FIRESTORE_EMULATOR_HOST")),
		OwnerEmail:            strings.TrimSpace(getenv("OWNER_EMAIL")),
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

	if len(errs) > 0 {
		return nil, fmt.Errorf("config: %w", errors.Join(errs...))
	}
	return cfg, nil
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
