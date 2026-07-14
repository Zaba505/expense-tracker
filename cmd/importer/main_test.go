package main

import (
	"errors"
	"flag"
	"io"
	"strings"
	"testing"
)

// noEnv is an environment with nothing in it.
func noEnv(string) string { return "" }

func TestParseFlags(t *testing.T) {
	t.Run("takes the project from the environment", func(t *testing.T) {
		// The same variable the server reads, so that a shell already pointed
		// at the emulator — or at the project — needs nothing said twice.
		env := map[string]string{
			"GCP_PROJECT":             "expense-tracker",
			"FIRESTORE_EMULATOR_HOST": "localhost:8085",
		}

		opts, err := parseFlags([]string{"-file", "history.parquet"}, func(k string) string { return env[k] }, io.Discard)
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}

		if opts.Project != "expense-tracker" {
			t.Errorf("project = %q, want it from GCP_PROJECT", opts.Project)
		}
		if opts.EmulatorHost != "localhost:8085" {
			t.Errorf("emulator host = %q, want it from FIRESTORE_EMULATOR_HOST", opts.EmulatorHost)
		}
		if opts.DryRun {
			t.Error("dry run is on by default; an import that writes when it was not asked to is the one mistake this binary cannot take back")
		}
	})

	t.Run("a flag beats the environment", func(t *testing.T) {
		env := map[string]string{"GCP_PROJECT": "from-the-environment"}

		opts, err := parseFlags(
			[]string{"-file", "history.parquet", "-project", "from-the-flag", "-dry-run"},
			func(k string) string { return env[k] },
			io.Discard,
		)
		if err != nil {
			t.Fatalf("parseFlags: %v", err)
		}

		if opts.Project != "from-the-flag" {
			t.Errorf("project = %q, want the flag to win", opts.Project)
		}
		if !opts.DryRun {
			t.Error("-dry-run was passed and did not take")
		}
	})

	t.Run("reports everything that is missing at once", func(t *testing.T) {
		// Both, not the first of the two: a first run with an empty environment
		// should not be a sequence of attempts, each told one more thing.
		_, err := parseFlags(nil, noEnv, io.Discard)
		if err == nil {
			t.Fatal("parseFlags with no file and no project = nil, want an error")
		}

		for _, want := range []string{"-file is required", "-project is required"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("parseFlags = %q, want it to mention %q", err, want)
			}
		}
	})

	t.Run("-h is not a failure", func(t *testing.T) {
		// main tells them apart by this, and exits 0 for a usage message.
		var usage strings.Builder

		_, err := parseFlags([]string{"-h"}, noEnv, &usage)
		if !errors.Is(err, flag.ErrHelp) {
			t.Fatalf("parseFlags(-h) = %v, want flag.ErrHelp", err)
		}
		if !strings.Contains(usage.String(), "Re-runs are safe") {
			t.Errorf("the usage message does not say that re-running is safe, which is the thing about this binary a reader most needs to know:\n%s", usage.String())
		}
	})
}
