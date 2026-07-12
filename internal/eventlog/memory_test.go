package eventlog_test

import (
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
	runEventStoreConformance(t, func(t *testing.T) eventlog.EventStore {
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
