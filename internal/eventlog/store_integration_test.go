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
	"crypto/rand"
	"fmt"
	"os"
	"strings"
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
func newStore(t *testing.T, projectID string) *eventlog.Store {
	t.Helper()

	host := os.Getenv("FIRESTORE_EMULATOR_HOST")
	if host == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set; start one with `dagger call emulator up --ports 8085:8085`")
	}

	store, err := eventlog.New(t.Context(), eventlog.Options{
		ProjectID:    projectID,
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

// TestStore is the Firestore half of the EventStore contract: the suite
// Memory runs, run against a real database. It is where the claims only a
// database can break get checked — that the ordering query is one
// Firestore will actually serve, and serve without a composite index;
// that an event survives the round-trip through the document schema with
// every field intact; and that the log's order stays total when the IDs
// breaking its ties are ones Firestore invented at random rather than
// ones a counter handed out in order.
func TestStore(t *testing.T) {
	runEventStoreConformance(t, func(t *testing.T) eventlog.EventStore {
		// A project per subtest, because the emulator namespaces data by
		// project and Load reads the whole log: one shared project would
		// have every subtest folding in the events of the ones before it.
		// The emulator conjures a database on first use, so an extra
		// project costs a name and nothing else.
		return newStore(t, isolatedProject(t))
	})
}

// isolatedProject invents a project ID for one subtest: a legal one
// (lowercase, dashes, 30 characters at the outside) that no sibling
// subtest and no earlier run of this suite has used.
//
// The test's name goes in so that data left in the emulator can be traced
// back to what wrote it. The random suffix goes in because the emulator
// outlives the test run — `dagger call emulator up` holds one open across
// many — and a second run of a test must not fold in the events its first
// run left lying there.
func isolatedProject(t *testing.T) string {
	t.Helper()

	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatalf("generating a project suffix: %v", err)
	}

	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return '-'
		}
	}, t.Name())

	// Keep the tail of the name when it will not fit: the front is
	// "teststore-", the same for every subtest, and the end is the part
	// that says which subtest this is.
	const maxName = 30 - len("test-") - len("-00112233")
	if len(name) > maxName {
		name = name[len(name)-maxName:]
	}

	return fmt.Sprintf("test-%s-%x", name, suffix)
}

// TestCheck is the readiness probe against a real database: it proves the
// client is wired to something that accepts a write and serves it back.
func TestCheck(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	store := newStore(t, emulatorProject)

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
