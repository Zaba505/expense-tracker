//go:build integration

// These tests run the importer against a real Firestore emulator. They are
// build-tagged because the `go test -race` stage has no emulator to talk to;
// run them with one bound:
//
//	dagger call integration-test
//
// or against an emulator you are already running:
//
//	FIRESTORE_EMULATOR_HOST=localhost:8085 go test -tags integration ./cmd/importer/
package main

import (
	"crypto/rand"
	"fmt"
	"os"
	"testing"

	"github.com/Zaba505/expense-tracker/internal/eventlog"
	"github.com/Zaba505/expense-tracker/internal/money"
)

// The idempotence the importer promises is Firestore's Create refusing a
// document that is already there. Memory stands in for that in every other
// test, and the conformance suite is what makes standing in legitimate — but
// the thing being stood in for is a database, and this is where the claim is
// made against one.
//
// It is worth the round trip for one specific reason: the key is not a field on
// the document, it is the document's name. Nothing in a unit test can prove
// that Firestore accepts the names this package invents, or that it hands them
// back on the way out — and if it did not, a re-import would recognize nothing
// and append five years of history a second time, into a log with no undo.

// newEmulatorStore connects to the emulator named by FIRESTORE_EMULATOR_HOST,
// skipping the test when there is not one.
//
// The project is fresh per test: the emulator namespaces data by project and
// the importer reads the whole log, so a shared one would have each test
// importing into the last one's events — and the emulator outlives the run, so
// "the last one" includes yesterday's.
func newEmulatorStore(t *testing.T) *eventlog.Store {
	t.Helper()

	host := os.Getenv("FIRESTORE_EMULATOR_HOST")
	if host == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST is not set; start one with `dagger call emulator up --ports 8085:8085`")
	}

	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatalf("generating a project suffix: %v", err)
	}

	store, err := eventlog.New(t.Context(), eventlog.Options{
		ProjectID:    fmt.Sprintf("test-importer-%x", suffix),
		EmulatorHost: host,
	})
	if err != nil {
		t.Fatalf("connecting to the emulator at %s: %v", host, err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("closing the store: %v", err)
		}
	})
	return store
}

// TestImportAgainstTheEmulator is the story's fourth acceptance criterion, and
// the run that matters: import the sheet into a database, import it again, and
// find the same events in it.
func TestImportAgainstTheEmulator(t *testing.T) {
	store := newEmulatorStore(t)
	events := sampleEvents(t)

	t.Log(mustImport(t, store, events, false))

	first := loadAll(t, store)
	if len(first) != len(events) {
		t.Fatalf("the log holds %d events after the import, want the %d rows of the file", len(first), len(events))
	}

	// The keys survived the round trip as document IDs. This is what a re-run
	// looks for, so if Firestore had altered them — or rejected them — the
	// re-import below would append everything a second time.
	for _, e := range events {
		if _, ok := find(first, importKey(e)); !ok {
			t.Errorf("no document in the emulator under the key for %s / %s", e.Month, e.Type)
		}
	}

	report := mustImport(t, store, events, false)

	second := loadAll(t, store)
	if len(second) != len(first) {
		t.Fatalf("the log holds %d events after the second import, want the %d it held after the first — the re-run duplicated the history", len(second), len(first))
	}
	for i := range first {
		if !equal(first[i], second[i]) {
			t.Errorf("event %d changed across the re-import:\n\tbefore %+v\n\tafter  %+v", i, first[i], second[i])
		}
	}
	assertReportSays(t, report, "appended          0")
	assertReportSays(t, report, "already imported  7")
}

// TestDryRunAgainstTheEmulatorWritesNothing: the flag has to mean the same
// thing against a database as it does against a map, and this is the only test
// that can say so — a dry run that wrote would write here.
func TestDryRunAgainstTheEmulatorWritesNothing(t *testing.T) {
	store := newEmulatorStore(t)

	report := mustImport(t, store, sampleEvents(t), true)

	if got := loadAll(t, store); len(got) != 0 {
		t.Fatalf("a dry run left %d documents in the emulator, want 0", len(got))
	}
	assertReportSays(t, report, "dry run: nothing was written.")
	assertReportSays(t, report, "to append         7")
}

// TestImportRefusesToOverwriteAgainstTheEmulator is the divergence path against
// a real database: the row is left alone, and it is Firestore that leaves it
// alone — a Create against a document that exists is refused by the database,
// not by a check this package does first.
func TestImportRefusesToOverwriteAgainstTheEmulator(t *testing.T) {
	store := newEmulatorStore(t)
	events := sampleEvents(t)

	mustImport(t, store, events, false)

	edited := slicesClone(events)
	edited[0].Amount = money.Cents(125_000)

	report, err := importInto(t, store, edited, false)
	if err == nil {
		t.Fatalf("importEvents of an edited sheet = nil, want a divergence reported\n%s", report)
	}

	stored, ok := find(loadAll(t, store), importKey(events[0]))
	if !ok {
		t.Fatal("the imported January rent is gone from the emulator")
	}
	if stored.Amount != events[0].Amount {
		t.Errorf("January rent in the database is now %s, want the %s that was imported", stored.Amount, events[0].Amount)
	}
}

// TestConcurrentImportsAgainstTheEmulator is the race the design turns on: two
// importers over the same file, at the same time, against one database.
//
// This is not a contrived scenario — it is a re-run started because the first
// one seemed stuck, and it is the case a read-then-write importer would get
// wrong. Both runs would read the same absence and both would append. Here the
// check is the write, so the database decides, and one of the two is told the
// key is taken.
func TestConcurrentImportsAgainstTheEmulator(t *testing.T) {
	store := newEmulatorStore(t)
	events := sampleEvents(t)

	const runs = 3
	errs := make(chan error, runs)
	for range runs {
		go func() {
			_, err := importInto(t, store, events, false)
			errs <- err
		}()
	}
	for range runs {
		if err := <-errs; err != nil {
			t.Errorf("a concurrent import failed: %v", err)
		}
	}

	// Every row exactly once, however the three runs interleaved.
	got := loadAll(t, store)
	if len(got) != len(events) {
		t.Fatalf("the log holds %d events after %d concurrent imports, want the %d rows of the file", len(got), runs, len(events))
	}
}
