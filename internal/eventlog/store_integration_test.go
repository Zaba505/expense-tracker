//go:build integration

// These tests talk to a real Firestore emulator. They are build-tagged
// because the z5labs pipeline's `go test -race` stage has no emulator to
// talk to; run them with one bound:
//
//	dagger call integration-test
//
// or against an emulator you are already running:
//
//	FIRESTORE_EMULATOR_HOST=localhost:8085 go test -tags integration ./internal/eventlog/
package eventlog_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Zaba505/expense-tracker/internal/eventlog"
)

// emulatorProject namespaces this test run's data inside the emulator.
const emulatorProject = "test-expense-tracker"

// newStore connects to the emulator named by FIRESTORE_EMULATOR_HOST,
// skipping the test when there isn't one — an emulator-less `go test
// -tags integration` should say why it did nothing, not hang dialing a
// service that is not there.
func newStore(t *testing.T) *eventlog.Store {
	t.Helper()

	host := os.Getenv("FIRESTORE_EMULATOR_HOST")
	if host == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set; start one with `dagger call emulator up --ports 8085:8085`")
	}

	store, err := eventlog.New(t.Context(), eventlog.Options{
		ProjectID:    emulatorProject,
		EmulatorHost: host,
	})
	if err != nil {
		t.Fatalf("connecting to the emulator at %s: %v", host, err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("closing store: %v", err)
		}
	})
	return store
}

// TestCheck is the readiness probe against a real database: it proves the
// client is wired to something that accepts a write and serves it back.
func TestCheck(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	store := newStore(t)

	if err := store.Check(ctx); err != nil {
		t.Fatalf("Check against the emulator: %v", err)
	}

	// Twice, because Check increments a counter on a shared document: the
	// second call exercises the merge-into-existing path, which is the one
	// a deployed instance takes every time but the first.
	if err := store.Check(ctx); err != nil {
		t.Fatalf("second Check: %v", err)
	}
}

// TestCheck_Unreachable pins the failure direction: a store pointed at a
// port with nothing behind it must report the failure rather than block
// until the caller's deadline is the only thing that saves it.
func TestCheck_Unreachable(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	// Port 1 on the loopback: nothing listens there, so the connection is
	// refused rather than left hanging.
	store, err := eventlog.New(ctx, eventlog.Options{
		ProjectID:    emulatorProject,
		EmulatorHost: "127.0.0.1:1",
	})
	if err != nil {
		t.Fatalf("New should not dial, so it should not fail: %v", err)
	}
	defer func() { _ = store.Close() }()

	if err := store.Check(ctx); err == nil {
		t.Fatal("Check against a dead address returned nil, want an error")
	}
}

// TestEvents_AppendAndRead is the story's acceptance criterion end to end:
// the app can append to the event log and read it back, locally, against
// the same client it will use in the cloud.
func TestEvents_AppendAndRead(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	store := newStore(t)

	type event struct {
		Action      string `firestore:"action"`
		Month       string `firestore:"month"`
		Type        string `firestore:"type"`
		AmountCents int64  `firestore:"amount_cents"`
	}
	want := event{Action: "add", Month: "2026-07", Type: "Groceries", AmountCents: 4_231}

	// An auto-id document, the shape the real append path will use: the
	// log is append-only, so nothing here names a document to overwrite.
	ref, _, err := store.Events().Add(ctx, want)
	if err != nil {
		t.Fatalf("appending an event: %v", err)
	}

	snap, err := ref.Get(ctx)
	if err != nil {
		t.Fatalf("reading back event %s: %v", ref.ID, err)
	}

	var got event
	if err := snap.DataTo(&got); err != nil {
		t.Fatalf("decoding event %s: %v", ref.ID, err)
	}
	if got != want {
		t.Errorf("event round-tripped as %+v, want %+v", got, want)
	}
}
