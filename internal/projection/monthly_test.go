package projection_test

import (
	"errors"
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
					{Month: "2026-07", Type: "Groceries"}: 1_100,
				},
			},
			"set only keeps the last value for one month and type": {
				events: []domain.Event{
					event(domain.ActionSet, "2026-07", "Groceries", 1_250),
					event(domain.ActionSet, "2026-07", "Groceries", -200),
					event(domain.ActionSet, "2026-07", "Groceries", 50),
				},
				want: projection.State{
					{Month: "2026-07", Type: "Groceries"}: 50,
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
					{Month: "2026-07", Type: "Groceries"}: 1_200,
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
					{Month: "2026-07", Type: "Groceries"}: 325,
					{Month: "2026-07", Type: "Rent"}:      1_100,
					{Month: "2026-08", Type: "Groceries"}: 50,
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
			return len(got) == 1 && got[projection.Key{Month: "2026-07", Type: "Groceries"}] == want
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
			return len(got) == 1 && got[projection.Key{Month: "2026-07", Type: "Groceries"}] == money.Cents(amounts[len(amounts)-1])
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

	t.Run("order is respected for add and set interplay", func(t *testing.T) {
		key := projection.Key{Month: "2026-07", Type: "Groceries"}

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
