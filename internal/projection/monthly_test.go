package projection_test

import (
	"errors"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/Zaba505/expense-tracker/internal/domain"
	"github.com/Zaba505/expense-tracker/internal/money"
	"github.com/Zaba505/expense-tracker/internal/projection"
)

func TestFold(t *testing.T) {
	t.Run("table cases", func(t *testing.T) {
		tests := map[string]struct {
			events []domain.Event
			want   projection.State
		}{
			"add only accumulates on one month and type": {
				events: []domain.Event{
					event(domain.ActionAdd, "2026-07", "Groceries", 1_250),
					event(domain.ActionAdd, "2026-07", "Groceries", -200),
					event(domain.ActionAdd, "2026-07", "Groceries", 50),
				},
				want: projection.State{
					{Month: "2026-07", Type: "Groceries", Direction: domain.DirectionExpense}: 1_100,
				},
			},
			"set only keeps the last value for one month and type": {
				events: []domain.Event{
					event(domain.ActionSet, "2026-07", "Groceries", 1_250),
					event(domain.ActionSet, "2026-07", "Groceries", -200),
					event(domain.ActionSet, "2026-07", "Groceries", 50),
				},
				want: projection.State{
					{Month: "2026-07", Type: "Groceries", Direction: domain.DirectionExpense}: 50,
				},
			},
			"set then add accumulates on top of the set amount": {
				events: []domain.Event{
					event(domain.ActionAdd, "2026-07", "Groceries", 300),
					event(domain.ActionSet, "2026-07", "Groceries", 1_000),
					event(domain.ActionAdd, "2026-07", "Groceries", 250),
					event(domain.ActionAdd, "2026-07", "Groceries", -50),
				},
				want: projection.State{
					{Month: "2026-07", Type: "Groceries", Direction: domain.DirectionExpense}: 1_200,
				},
			},
			"keys stay independent across months and types": {
				events: []domain.Event{
					event(domain.ActionAdd, "2026-07", "Groceries", 300),
					event(domain.ActionSet, "2026-07", "Rent", 1_200),
					event(domain.ActionAdd, "2026-08", "Groceries", 50),
					event(domain.ActionAdd, "2026-07", "Groceries", 25),
					event(domain.ActionAdd, "2026-07", "Rent", -100),
				},
				want: projection.State{
					{Month: "2026-07", Type: "Groceries", Direction: domain.DirectionExpense}: 325,
					{Month: "2026-07", Type: "Rent", Direction: domain.DirectionExpense}:      1_100,
					{Month: "2026-08", Type: "Groceries", Direction: domain.DirectionExpense}: 50,
				},
			},
		}

		for name, tt := range tests {
			t.Run(name, func(t *testing.T) {
				got, err := projection.Fold(tt.events)
				if err != nil {
					t.Fatalf("Fold() error = %v", err)
				}
				if !equalState(got, tt.want) {
					t.Errorf("Fold() = %#v, want %#v", got, tt.want)
				}
			})
		}
	})

	t.Run("property: add only sums all amounts for one key", func(t *testing.T) {
		if err := quick.Check(func(amounts []int64) bool {
			events := make([]domain.Event, 0, len(amounts))
			var want money.Cents
			for _, amount := range amounts {
				events = append(events, event(domain.ActionAdd, "2026-07", "Groceries", money.Cents(amount)))
				want += money.Cents(amount)
			}

			got, err := projection.Fold(events)
			if err != nil {
				return false
			}
			if len(amounts) == 0 {
				return len(got) == 0
			}
			return len(got) == 1 && got[projection.Key{Month: "2026-07", Type: "Groceries", Direction: domain.DirectionExpense}] == want
		}, nil); err != nil {
			t.Error(err)
		}
	})

	t.Run("property: set only keeps the last amount for one key", func(t *testing.T) {
		if err := quick.Check(func(amounts []int64) bool {
			events := make([]domain.Event, 0, len(amounts))
			for _, amount := range amounts {
				events = append(events, event(domain.ActionSet, "2026-07", "Groceries", money.Cents(amount)))
			}

			got, err := projection.Fold(events)
			if err != nil {
				return false
			}
			if len(amounts) == 0 {
				return len(got) == 0
			}
			return len(got) == 1 && got[projection.Key{Month: "2026-07", Type: "Groceries", Direction: domain.DirectionExpense}] == money.Cents(amounts[len(amounts)-1])
		}, nil); err != nil {
			t.Error(err)
		}
	})

	t.Run("unknown actions fail loudly", func(t *testing.T) {
		got, err := projection.Fold([]domain.Event{
			event(domain.ActionAdd, "2026-07", "Groceries", 300),
			event("multiply", "2026-07", "Groceries", 2),
		})
		if got != nil {
			t.Errorf("Fold() returned partial state %#v, want nil on an unknown action", got)
		}
		if !errors.Is(err, projection.ErrUnknownAction) {
			t.Fatalf("Fold() error = %v, want ErrUnknownAction", err)
		}
	})

	t.Run("unknown directions fail loudly, at the offending event", func(t *testing.T) {
		// The direction is half of a cell's identity, so an unvetted one is
		// not a cell the totals get to meet later — it is a bad event, and it
		// has to be refused where ErrUnknownAction refuses one. A direction
		// that folded "fine" and only detonated in the rollup would take every
		// good month down with the bad one.
		bad := event(domain.ActionSet, "2026-07", "Bonus", 50_000)
		bad.ID = "evt-42"
		bad.Direction = "Income" // capitalized: the shape a CSV import produces

		got, err := projection.Fold([]domain.Event{
			event(domain.ActionSet, "2020-01", "Rent", 100_000),
			bad,
		})
		if got != nil {
			t.Errorf("Fold() returned partial state %#v, want nil on an unknown direction", got)
		}
		if !errors.Is(err, projection.ErrUnknownDirection) {
			t.Fatalf("Fold() error = %v, want ErrUnknownDirection", err)
		}
		// An append-only log has no edit: naming the event is the only way the
		// owner can find the record to append a correction against.
		if msg := err.Error(); !strings.Contains(msg, "evt-42") || !strings.Contains(msg, "Bonus") {
			t.Errorf("Fold() error = %q, want it to name the offending event (id evt-42, type Bonus)", msg)
		}
	})

	t.Run("order is respected for add and set interplay", func(t *testing.T) {
		key := projection.Key{Month: "2026-07", Type: "Groceries", Direction: domain.DirectionExpense}

		addThenSet, err := projection.Fold([]domain.Event{
			event(domain.ActionAdd, "2026-07", "Groceries", 300),
			event(domain.ActionSet, "2026-07", "Groceries", 1_000),
		})
		if err != nil {
			t.Fatalf("Fold(add then set): %v", err)
		}

		setThenAdd, err := projection.Fold([]domain.Event{
			event(domain.ActionSet, "2026-07", "Groceries", 1_000),
			event(domain.ActionAdd, "2026-07", "Groceries", 300),
		})
		if err != nil {
			t.Fatalf("Fold(set then add): %v", err)
		}

		if got := addThenSet[key]; got != 1_000 {
			t.Errorf("Fold(add then set)[%+v] = %s, want 10.00", key, got)
		}
		if got := setThenAdd[key]; got != 1_300 {
			t.Errorf("Fold(set then add)[%+v] = %s, want 13.00", key, got)
		}
		if equalState(addThenSet, setThenAdd) {
			t.Errorf("Fold() gave the same state for two event orders; want order to matter when actions differ")
		}
	})

	t.Run("a rename replays the history behind it under the target type", func(t *testing.T) {
		got, err := projection.Fold([]domain.Event{
			event(domain.ActionAdd, "2026-01", "Fuel", 40_00),
			event(domain.ActionAdd, "2026-01", "Gas", 10_00),
			event(domain.ActionAdd, "2026-02", "Fuel", 15_00),
			renameAt("Fuel", "Gas", time.Date(2026, 7, 12, 15, 5, 0, 0, time.UTC)),
		})
		if err != nil {
			t.Fatalf("Fold() error = %v", err)
		}

		// Every cent survives the merge. That is the whole contract: a rename
		// changes the name history is read under, never the money in it.
		want := projection.State{
			{Month: "2026-01", Type: "Gas", Direction: domain.DirectionExpense}: 50_00,
			{Month: "2026-02", Type: "Gas", Direction: domain.DirectionExpense}: 15_00,
		}
		if !equalState(got, want) {
			t.Errorf("Fold() = %#v, want %#v", got, want)
		}
	})

	t.Run("a rename does not reach the entries recorded after it", func(t *testing.T) {
		// The type field is free text, so the old name can be typed again. When
		// it is, it names a new type — not more of the one it was renamed to.
		// An alias applied to the whole log would silently swallow this entry,
		// and the owner would have no way to see that it had.
		got, err := projection.Fold([]domain.Event{
			event(domain.ActionAdd, "2026-01", "Fuel", 40_00),
			renameAt("Fuel", "Gas", time.Date(2026, 2, 1, 9, 0, 0, 0, time.UTC)),
			event(domain.ActionAdd, "2026-03", "Fuel", 5_00),
		})
		if err != nil {
			t.Fatalf("Fold() error = %v", err)
		}

		want := projection.State{
			{Month: "2026-01", Type: "Gas", Direction: domain.DirectionExpense}:  40_00,
			{Month: "2026-03", Type: "Fuel", Direction: domain.DirectionExpense}: 5_00,
		}
		if !equalState(got, want) {
			t.Errorf("Fold() = %#v, want %#v", got, want)
		}
	})

	t.Run("renames chain, and the last one wins", func(t *testing.T) {
		got, err := projection.Fold([]domain.Event{
			event(domain.ActionAdd, "2026-01", "Fuel", 40_00),
			renameAt("Fuel", "Gas", time.Date(2026, 2, 1, 9, 0, 0, 0, time.UTC)),
			renameAt("Gas", "Petrol", time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)),
		})
		if err != nil {
			t.Fatalf("Fold() error = %v", err)
		}

		want := projection.State{
			{Month: "2026-01", Type: "Petrol", Direction: domain.DirectionExpense}: 40_00,
		}
		if !equalState(got, want) {
			t.Errorf("Fold() = %#v, want %#v", got, want)
		}
	})

	t.Run("a rename is walked back by renaming it back", func(t *testing.T) {
		// The append-only answer to a rename made in error. It works because a
		// rename only reaches the past: by the time the second one is recorded,
		// history says "Gas", and renaming Gas to Fuel returns it.
		got, err := projection.Fold([]domain.Event{
			event(domain.ActionAdd, "2026-01", "Fuel", 40_00),
			renameAt("Fuel", "Gas", time.Date(2026, 2, 1, 9, 0, 0, 0, time.UTC)),
			renameAt("Gas", "Fuel", time.Date(2026, 2, 2, 9, 0, 0, 0, time.UTC)),
		})
		if err != nil {
			t.Fatalf("Fold() error = %v", err)
		}

		want := projection.State{
			{Month: "2026-01", Type: "Fuel", Direction: domain.DirectionExpense}: 40_00,
		}
		if !equalState(got, want) {
			t.Errorf("Fold() = %#v, want %#v", got, want)
		}
	})

	t.Run("direction splits one month and type into two cells", func(t *testing.T) {
		// A grocery rebate, recorded as income against the same type, is not
		// a discount on the grocery bill: the $10 still went out and the
		// $2.50 still came in, and a rollup has to be able to say so.
		got, err := projection.Fold([]domain.Event{
			event(domain.ActionAdd, "2026-07", "Groceries", 1_000),
			incomeEvent(domain.ActionAdd, "2026-07", "Groceries", 250),
		})
		if err != nil {
			t.Fatalf("Fold() error = %v", err)
		}

		want := projection.State{
			{Month: "2026-07", Type: "Groceries", Direction: domain.DirectionExpense}: 1_000,
			{Month: "2026-07", Type: "Groceries", Direction: domain.DirectionIncome}:  250,
		}
		if !equalState(got, want) {
			t.Errorf("Fold() = %#v, want %#v", got, want)
		}
	})

	t.Run("set supersedes only its own direction", func(t *testing.T) {
		got, err := projection.Fold([]domain.Event{
			event(domain.ActionSet, "2026-07", "Groceries", 1_000),
			incomeEvent(domain.ActionSet, "2026-07", "Groceries", 250),
		})
		if err != nil {
			t.Fatalf("Fold() error = %v", err)
		}

		key := projection.Key{Month: "2026-07", Type: "Groceries", Direction: domain.DirectionExpense}
		if amount, ok := got[key]; !ok || amount != 1_000 {
			t.Errorf("Fold()[%+v] = %s (present = %t), want 10.00; an income set must not overwrite the expense cell", key, amount, ok)
		}
	})

	t.Run("unnormalized events fold into the canonical cell", func(t *testing.T) {
		// Events that never went through a store — the importer's, a
		// hand-built one — still have to land where a store's would. An
		// untrimmed type or a defaulted direction that produced a cell of its
		// own would be an amount the user entered and no total ever counts.
		bare := domain.Event{
			Action:     domain.ActionAdd,
			Month:      "2026-07",
			Type:       "  Groceries  ",
			Amount:     300,
			RecordedAt: time.Date(2026, 7, 12, 15, 4, 5, 0, time.UTC),
		}

		got, err := projection.Fold([]domain.Event{
			event(domain.ActionAdd, "2026-07", "Groceries", 1_000),
			bare,
		})
		if err != nil {
			t.Fatalf("Fold() error = %v", err)
		}

		want := projection.State{
			{Month: "2026-07", Type: "Groceries", Direction: domain.DirectionExpense}: 1_300,
		}
		if !equalState(got, want) {
			t.Errorf("Fold() = %#v, want %#v", got, want)
		}
	})
}

func event(action domain.Action, month, typ string, amount money.Cents) domain.Event {
	return domain.Event{
		Action:     action,
		Month:      month,
		Type:       typ,
		Amount:     amount,
		Direction:  domain.DirectionExpense,
		RecordedAt: time.Date(2026, 7, 12, 15, 4, 5, 0, time.UTC),
	}
}

func incomeEvent(action domain.Action, month, typ string, amount money.Cents) domain.Event {
	e := event(action, month, typ, amount)
	e.Direction = domain.DirectionIncome
	return e
}

func equalState(a, b projection.State) bool {
	if len(a) != len(b) {
		return false
	}
	for key, want := range b {
		if got, ok := a[key]; !ok || got != want {
			return false
		}
	}
	return true
}
