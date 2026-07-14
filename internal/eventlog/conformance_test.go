package eventlog_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Zaba505/expense-tracker/internal/domain"
	"github.com/Zaba505/expense-tracker/internal/eventlog"
	"github.com/Zaba505/expense-tracker/internal/money"
)

// This file is the contract of an EventStore, written once and run twice:
// against Memory (memory_test.go, always) and against a real Firestore
// emulator (store_integration_test.go, under the `integration` tag).
//
// Running one suite against both is the point. Memory is only worth
// testing against if it behaves like the database it stands in for, and
// the Firestore store is only worth trusting if it obeys the rules the
// log claims — the same defaults, the same refusals, the same order. A
// behavior that only one of them has is a bug in whichever one is alone.

// storeFactory builds an empty store to run one subtest against. Each
// subtest gets its own, because Load reads the whole log: a store shared
// with the previous subtest would hand this one that subtest's events.
type storeFactory func(t *testing.T) eventlog.EventStore

// runEventStoreConformance runs the whole contract against one
// implementation.
func runEventStoreConformance(t *testing.T, newStore storeFactory) {
	t.Run("Append", func(t *testing.T) {
		t.Run("assigns an ID", func(t *testing.T) {
			store := newStore(t)

			got, err := store.Append(t.Context(), event())
			if err != nil {
				t.Fatalf("Append: %v", err)
			}

			if got.ID == "" {
				t.Error("Append returned an event with no ID; the ID is what the load order breaks ties on, and what a correction refers back to")
			}
		})

		t.Run("stamps recordedAt when the caller leaves it unset", func(t *testing.T) {
			store := newStore(t)

			before := time.Now().UTC().Add(-time.Second)
			e := event()
			e.RecordedAt = time.Time{}

			got, err := store.Append(t.Context(), e)
			if err != nil {
				t.Fatalf("Append: %v", err)
			}
			after := time.Now().UTC().Add(time.Second)

			if got.RecordedAt.Before(before) || got.RecordedAt.After(after) {
				t.Errorf("Append stamped recordedAt %s, want a time between %s and %s", got.RecordedAt, before, after)
			}
		})

		t.Run("keeps a recordedAt the caller supplied", func(t *testing.T) {
			// The importer depends on this: five years of replayed history
			// have to keep the order they happened in, not all land at the
			// instant of the import.
			store := newStore(t)

			e := event()
			e.RecordedAt = time.Date(2021, 3, 14, 9, 26, 53, 0, time.UTC)

			got, err := store.Append(t.Context(), e)
			if err != nil {
				t.Fatalf("Append: %v", err)
			}

			if !got.RecordedAt.Equal(e.RecordedAt) {
				t.Errorf("Append moved recordedAt to %s, want the supplied %s", got.RecordedAt, e.RecordedAt)
			}
		})

		t.Run("defaults an unstated direction to expense", func(t *testing.T) {
			store := newStore(t)

			e := event()
			e.Direction = ""

			got, err := store.Append(t.Context(), e)
			if err != nil {
				t.Fatalf("Append: %v", err)
			}
			if got.Direction != domain.DirectionExpense {
				t.Errorf("Append returned direction %q, want %q", got.Direction, domain.DirectionExpense)
			}

			// And it is the stored event that defaulted, not just the
			// returned copy: what a fold reads is what matters.
			stored := loadAll(t, store)
			if len(stored) != 1 {
				t.Fatalf("loaded %d events, want 1", len(stored))
			}
			if stored[0].Direction != domain.DirectionExpense {
				t.Errorf("the stored event's direction is %q, want %q", stored[0].Direction, domain.DirectionExpense)
			}
		})

		t.Run("refuses an event it cannot vouch for", func(t *testing.T) {
			store := newStore(t)

			e := event()
			e.Month = "2026-13"

			if _, err := store.Append(t.Context(), e); !errors.Is(err, domain.ErrInvalidEvent) {
				t.Errorf("Append gave %v, want ErrInvalidEvent — a bad event in an append-only log cannot be fixed afterwards", err)
			}

			if got := loadAll(t, store); len(got) != 0 {
				t.Errorf("the log holds %d events after a refused append, want 0", len(got))
			}
		})

		t.Run("reports a cancelled context as cancellation", func(t *testing.T) {
			// A handler whose request went away and a database that fell
			// over are different problems — one is nothing to worry about,
			// the other is an outage — and a caller can only tell them
			// apart if both stores report cancellation the same way.
			store := newStore(t)

			ctx, cancel := context.WithCancel(t.Context())
			cancel()

			_, err := store.Append(ctx, event())
			if !errors.Is(err, context.Canceled) {
				t.Errorf("Append on a cancelled context gave %v, want context.Canceled", err)
			}
			wantAttributed(t, "Append on a cancelled context", err)
		})

		t.Run("refuses a bad event even on a cancelled context", func(t *testing.T) {
			// Which failure a store reports when both are true has to be
			// the same failure in both stores, or a handler that answers
			// 400 to ErrInvalidEvent answers it in tests and drops the
			// request in production. The event is bad either way, so the
			// event is what is reported.
			store := newStore(t)

			ctx, cancel := context.WithCancel(t.Context())
			cancel()

			e := event()
			e.Month = "2026-13"

			if _, err := store.Append(ctx, e); !errors.Is(err, domain.ErrInvalidEvent) {
				t.Errorf("Append of an invalid event on a cancelled context gave %v, want ErrInvalidEvent", err)
			}
		})

		t.Run("refuses an event that was already appended", func(t *testing.T) {
			// The only way to hand a store an event with an ID is to have
			// gotten it from a store. Honoring the ID would be an in-place
			// edit of history; ignoring it would duplicate the fact.
			store := newStore(t)

			appended, err := store.Append(t.Context(), event())
			if err != nil {
				t.Fatalf("Append: %v", err)
			}

			appended.Amount = money.Cents(999_999)
			if _, err := store.Append(t.Context(), appended); !errors.Is(err, eventlog.ErrEventAppended) {
				t.Errorf("Append gave %v, want ErrEventAppended", err)
			}

			got := loadAll(t, store)
			if len(got) != 1 {
				t.Fatalf("the log holds %d events, want the 1 that was appended", len(got))
			}
			if got[0].Amount != event().Amount {
				t.Errorf("the stored amount is %s, want the original %s; a re-append rewrote history", got[0].Amount, event().Amount)
			}
		})
	})

	t.Run("Load", func(t *testing.T) {
		t.Run("yields nothing from an empty log", func(t *testing.T) {
			store := newStore(t)

			if got := loadAll(t, store); len(got) != 0 {
				t.Errorf("an empty log yielded %d events, want 0", len(got))
			}
		})

		t.Run("round-trips every field", func(t *testing.T) {
			store := newStore(t)

			want := domain.Event{
				Action:     domain.ActionSet,
				Month:      "2021-03",
				Type:       "Rent",
				ToType:     "Housing",
				Amount:     money.Cents(-1_234_56),
				Direction:  domain.DirectionIncome,
				Note:       "double-counted in the sheet",
				RefEventID: "0IZmA5MFhTHhszo6XVYy",
				RecordedAt: time.Date(2021, 3, 14, 9, 26, 53, 589_793_000, time.UTC),
			}

			appended, err := store.Append(t.Context(), want)
			if err != nil {
				t.Fatalf("Append: %v", err)
			}

			got := loadAll(t, store)
			if len(got) != 1 {
				t.Fatalf("loaded %d events, want 1", len(got))
			}
			if !equal(got[0], appended) {
				t.Errorf("the log yielded\n\t%+v\nbut Append said it stored\n\t%+v", got[0], appended)
			}

			want.ID = appended.ID
			if !equal(got[0], want) {
				t.Errorf("the log yielded\n\t%+v\nwant\n\t%+v", got[0], want)
			}
		})

		t.Run("orders by recordedAt, not by append order", func(t *testing.T) {
			// Appended newest-first, on purpose: a fold that sees a
			// correction before the event it corrects computes a different
			// total, so the order the log yields cannot be the order the
			// writes happened to arrive in.
			store := newStore(t)

			base := time.Date(2026, 7, 12, 15, 4, 5, 0, time.UTC)
			for i, offset := range []time.Duration{2 * time.Hour, 0, time.Hour} {
				e := event()
				e.Note = []string{"third", "first", "second"}[i]
				e.RecordedAt = base.Add(offset)

				if _, err := store.Append(t.Context(), e); err != nil {
					t.Fatalf("appending %s: %v", e.Note, err)
				}
			}

			var notes []string
			for _, e := range loadAll(t, store) {
				notes = append(notes, e.Note)
			}

			want := []string{"first", "second", "third"}
			if !slices.Equal(notes, want) {
				t.Errorf("the log yielded %v, want %v", notes, want)
			}
		})

		t.Run("breaks ties on recordedAt by ID", func(t *testing.T) {
			// Timestamps are microsecond-resolution, so two events can
			// share one. Without a tiebreak their order would be the
			// database's to choose, and a fold would be reproducible only
			// by luck. Which ID sorts first does not matter; that the
			// order is total, and the same on every load, does.
			store := newStore(t)

			tied := time.Date(2026, 7, 12, 15, 4, 5, 0, time.UTC)
			const n = 5
			for range n {
				e := event()
				e.RecordedAt = tied

				if _, err := store.Append(t.Context(), e); err != nil {
					t.Fatalf("Append: %v", err)
				}
			}

			var ids []string
			for _, e := range loadAll(t, store) {
				ids = append(ids, e.ID)
			}
			if len(ids) != n {
				t.Fatalf("loaded %d events, want %d", len(ids), n)
			}
			if !slices.IsSorted(ids) {
				t.Errorf("events sharing a recordedAt came back with IDs in the order %v, want them sorted; the load order is not total", ids)
			}

			// And the same on the next load, which is the property a fold
			// on a second instance actually depends on.
			var again []string
			for _, e := range loadAll(t, store) {
				again = append(again, e.ID)
			}
			if !slices.Equal(ids, again) {
				t.Errorf("a second load yielded %v, want the same order as the first, %v", again, ids)
			}
		})

		t.Run("stops when the consumer stops", func(t *testing.T) {
			// A fold may bail early — a projection that has what it needs,
			// a handler whose request was cancelled. Breaking the range
			// must end the stream rather than run it to completion.
			store := newStore(t)

			base := time.Date(2026, 7, 12, 15, 4, 5, 0, time.UTC)
			for i := range 3 {
				e := event()
				e.RecordedAt = base.Add(time.Duration(i) * time.Hour)

				if _, err := store.Append(t.Context(), e); err != nil {
					t.Fatalf("Append: %v", err)
				}
			}

			seen := 0
			for _, err := range store.Load(t.Context()) {
				if err != nil {
					t.Fatalf("Load: %v", err)
				}
				seen++
				break
			}

			if seen != 1 {
				t.Errorf("the consumer saw %d events after breaking at the first, want 1", seen)
			}
		})

		t.Run("reports a cancelled context instead of a short log", func(t *testing.T) {
			// A truncated fold is not a smaller answer, it is a wrong one:
			// the totals would simply be missing whatever the log did not
			// get to. Cancellation has to surface as an error.
			//
			// Run against an empty log as well as a full one, because an
			// empty log is where a store is most tempted to answer without
			// looking — and "no events" is the one wrong answer that looks
			// exactly like a right one. A new user's log is empty.
			for name, seed := range map[string]int{"an empty log": 0, "a log with events": 1} {
				t.Run(name, func(t *testing.T) {
					store := newStore(t)

					for range seed {
						if _, err := store.Append(t.Context(), event()); err != nil {
							t.Fatalf("Append: %v", err)
						}
					}

					ctx, cancel := context.WithCancel(t.Context())
					cancel()

					var loadErr error
					var loaded int
					for _, err := range store.Load(ctx) {
						if err != nil {
							loadErr = err
							break
						}
						loaded++
					}

					if !errors.Is(loadErr, context.Canceled) {
						t.Errorf("Load on a cancelled context yielded %d events and the error %v, want context.Canceled", loaded, loadErr)
					}
					wantAttributed(t, "Load on a cancelled context", loadErr)
				})
			}
		})
	})
}

// wantAttributed fails unless err names the package it came from.
//
// Matching on a message is usually a brittle test, and this is the one
// place it earns its keep: it pins the only difference between the stores
// that errors.Is cannot see. Both stores' errors end up on the same log
// line, and one that reads "context canceled" and nothing else leaves the
// reader to guess which of a request's several moving parts gave up.
// Whether a store wraps or returns ctx.Err() raw is invisible to every
// other assertion in this suite — the two stores disagreed about exactly
// that, and every errors.Is here passed anyway.
func wantAttributed(t *testing.T, op string, err error) {
	t.Helper()

	const prefix = "eventlog: "
	if err == nil || !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("%s gave the error %v, want it to name the package that produced it (%q...)", op, err, prefix)
	}
}

// event is a valid event to append: the tests vary one field of it at a
// time so a failure names what broke.
func event() domain.Event {
	return domain.Event{
		Action:     domain.ActionAdd,
		Month:      "2026-07",
		Type:       "Groceries",
		Amount:     money.Cents(4_231),
		Direction:  domain.DirectionExpense,
		RecordedAt: time.Date(2026, 7, 12, 15, 4, 5, 0, time.UTC),
	}
}

// loadAll drains the log, failing the test on the first error — which is
// what a fold does, and the reason Load promises to stop at one.
func loadAll(t *testing.T, store eventlog.EventStore) []domain.Event {
	t.Helper()

	var events []domain.Event
	for e, err := range store.Load(t.Context()) {
		if err != nil {
			t.Fatalf("loading the log: %v", err)
		}
		events = append(events, e)
	}
	return events
}

// equal compares two events. It exists because time.Time is not usefully
// comparable with ==: two instants that Equal each other can differ in
// their wall-clock representation, and a store that hands back a
// different one has not done anything wrong.
func equal(a, b domain.Event) bool {
	return a.ID == b.ID &&
		a.Action == b.Action &&
		a.Month == b.Month &&
		a.Type == b.Type &&
		a.ToType == b.ToType &&
		a.Amount == b.Amount &&
		a.Direction == b.Direction &&
		a.Note == b.Note &&
		a.RefEventID == b.RefEventID &&
		a.RecordedAt.Equal(b.RecordedAt)
}
