package eventlog_test

import (
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Zaba505/expense-tracker/internal/eventlog"
)

// TestMemory holds the in-memory log to the same contract as the
// Firestore one. It is the half of the suite that runs on every `go test`
// — the emulator half needs a database and a build tag — so it is what
// keeps the contract honest during ordinary work.
func TestMemory(t *testing.T) {
	runEventStoreConformance(t, func(t *testing.T) eventlog.UniqueAppender {
		return eventlog.NewMemory()
	})
}

// TestMemory_ConcurrentAppend is the claim in Memory's doc comment, under
// -race: the log is what a web handler and a fold share, so a store that
// only works one goroutine at a time would be a store that works until
// the first two requests overlap.
//
// Firestore is concurrency-safe on its own account and is not worth a
// round-trip to prove it, so this test is Memory's alone.
func TestMemory_ConcurrentAppend(t *testing.T) {
	store := eventlog.NewMemory()

	const appenders = 32
	var wg sync.WaitGroup
	wg.Add(appenders)

	for i := range appenders {
		go func() {
			defer wg.Done()

			e := event()
			// Half the events share a timestamp and half do not, so the
			// sort that Load runs is exercised against a log that was
			// built by racing writers rather than an ordered one.
			e.RecordedAt = e.RecordedAt.Add(time.Duration(i%2) * time.Hour)

			if _, err := store.Append(t.Context(), e); err != nil {
				t.Errorf("Append: %v", err)
			}
		}()
	}
	wg.Wait()

	got := loadAll(t, store)
	if len(got) != appenders {
		t.Fatalf("the log holds %d events after %d concurrent appends, want %d — a lost append is a lost expense", len(got), appenders, appenders)
	}

	ids := make([]string, 0, len(got))
	for _, e := range got {
		ids = append(ids, e.ID)
	}
	slices.Sort(ids)
	if unique := slices.Compact(ids); len(unique) != appenders {
		t.Errorf("the %d appends produced %d distinct IDs; two events sharing an ID are one event to everything downstream", appenders, len(unique))
	}
}

// TestMemory_ConcurrentAppendUnique holds the keyed path to the promise
// that makes it worth having: exactly one of the writers under a key wins,
// and the rest are told the key is taken.
//
// Racing writers under one key are not a hypothetical the importer can
// dismiss. A re-run started while the first run is still going, two
// terminals, a retry that overlaps the request it is retrying — each of
// them is two appends of the same row, and an append-only log has no way
// to take the loser back. Firestore's Create decides the race in the
// database; this is the same claim made of the store that stands in for it
// in every test the importer has.
func TestMemory_ConcurrentAppendUnique(t *testing.T) {
	store := eventlog.NewMemory()

	const (
		appenders = 32
		key       = "import-7d1f0c2a"
	)

	var (
		wg         sync.WaitGroup
		mu         sync.Mutex
		appended   int
		duplicates int
	)
	wg.Add(appenders)

	for range appenders {
		go func() {
			defer wg.Done()

			_, err := store.AppendUnique(t.Context(), key, event())

			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				appended++
			case errors.Is(err, eventlog.ErrDuplicateKey):
				duplicates++
			default:
				t.Errorf("AppendUnique: %v", err)
			}
		}()
	}
	wg.Wait()

	if appended != 1 {
		t.Errorf("%d of %d racing appends under one key succeeded, want exactly 1", appended, appenders)
	}
	if duplicates != appenders-1 {
		t.Errorf("%d appends were told the key was taken, want %d", duplicates, appenders-1)
	}
	if got := loadAll(t, store); len(got) != 1 {
		t.Errorf("the log holds %d events, want 1 — a racing re-run duplicated a row that cannot be deleted", len(got))
	}
}
