package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/Zaba505/expense-tracker/internal/domain"
	"github.com/Zaba505/expense-tracker/internal/money"
)

// valid is the event the tests below vary one field of at a time, so that
// a failure names the field that broke rather than the whole struct.
func valid() domain.Event {
	return domain.Event{
		Action:     domain.ActionAdd,
		Month:      "2026-07",
		Type:       "Groceries",
		Amount:     money.Cents(4_231),
		Direction:  domain.DirectionExpense,
		RecordedAt: time.Date(2026, 7, 12, 15, 4, 5, 0, time.UTC),
	}
}

func TestEvent_Normalize(t *testing.T) {
	t.Run("an unset direction is an expense", func(t *testing.T) {
		e := valid()
		e.Direction = ""

		if got := e.Normalize().Direction; got != domain.DirectionExpense {
			t.Errorf("Normalize() left direction %q, want %q — an entry that says nothing about direction is money going out", got, domain.DirectionExpense)
		}
	})

	t.Run("a stated direction is left alone", func(t *testing.T) {
		e := valid()
		e.Direction = domain.DirectionIncome

		if got := e.Normalize().Direction; got != domain.DirectionIncome {
			t.Errorf("Normalize() changed direction to %q, want %q", got, domain.DirectionIncome)
		}
	})

	t.Run("type and note lose surrounding whitespace", func(t *testing.T) {
		e := valid()
		e.Type = "  Groceries \t"
		e.Note = "  corrects the March total  "

		got := e.Normalize()
		if got.Type != "Groceries" {
			t.Errorf("Normalize() left type %q, want %q — an untrimmed type is a second type that shadows the real one", got.Type, "Groceries")
		}
		if got.Note != "corrects the March total" {
			t.Errorf("Normalize() left note %q, want %q", got.Note, "corrects the March total")
		}
	})

	t.Run("recordedAt lands in UTC at microsecond resolution", func(t *testing.T) {
		// A zone east of UTC and a nanosecond-precise instant: the two
		// things a store cannot round-trip, so the log must not keep them.
		zone := time.FixedZone("UTC+2", 2*60*60)
		e := valid()
		e.RecordedAt = time.Date(2026, 7, 12, 17, 4, 5, 123_456_789, zone)

		got := e.Normalize().RecordedAt
		want := time.Date(2026, 7, 12, 15, 4, 5, 123_456_000, time.UTC)
		if !got.Equal(want) {
			t.Errorf("Normalize() gave recordedAt %s, want %s", got, want)
		}
		if _, offset := got.Zone(); offset != 0 {
			t.Errorf("Normalize() left recordedAt in a zone %d seconds off UTC; the log's order is only total if every timestamp is on one clock", offset)
		}
	})

	t.Run("an unset recordedAt stays unset", func(t *testing.T) {
		// Normalize must not stamp a clock: that is the store's job, and a
		// store distinguishes "the caller supplied a time" from "fill one
		// in" by asking whether this field is still zero.
		e := valid()
		e.RecordedAt = time.Time{}

		if got := e.Normalize().RecordedAt; !got.IsZero() {
			t.Errorf("Normalize() stamped recordedAt %s onto an event that had none; the store decides that", got)
		}
	})
}

func TestEvent_Validate(t *testing.T) {
	t.Run("accepts", func(t *testing.T) {
		tests := map[string]func(*domain.Event){
			"an added expense": func(e *domain.Event) {},
			"a set, as the import writes it": func(e *domain.Event) {
				e.Action = domain.ActionSet
			},
			"income": func(e *domain.Event) {
				e.Direction = domain.DirectionIncome
			},
			"a negative amount, walking an overstatement back": func(e *domain.Event) {
				e.Amount = money.Cents(-4_231)
			},
			"a zero amount, saying a type had no spend": func(e *domain.Event) {
				e.Amount = 0
			},
			"a note and the event it corrects": func(e *domain.Event) {
				e.Note = "double-counted"
				e.RefEventID = "0IZmA5MFhTHhszo6XVYy"
			},
			"the first month of a year": func(e *domain.Event) {
				e.Month = "2026-01"
			},
			"the last month of a year": func(e *domain.Event) {
				e.Month = "2026-12"
			},
			"a month five years back, as the import replays it": func(e *domain.Event) {
				e.Month = "2021-03"
			},
		}

		for name, mutate := range tests {
			t.Run(name, func(t *testing.T) {
				e := valid()
				mutate(&e)

				if err := e.Validate(); err != nil {
					t.Errorf("Validate() rejected %+v: %v", e, err)
				}
			})
		}
	})

	t.Run("rejects", func(t *testing.T) {
		tests := map[string]func(*domain.Event){
			"an empty action": func(e *domain.Event) {
				e.Action = ""
			},
			"an action nothing folds": func(e *domain.Event) {
				// The set of actions is open, but it is opened by adding a
				// constant and a fold case — not by writing whatever a
				// caller passes into a log that can never be edited.
				e.Action = "subtract"
			},
			"an action in the wrong case": func(e *domain.Event) {
				e.Action = "ADD"
			},
			"an empty month": func(e *domain.Event) {
				e.Month = ""
			},
			"a month that does not exist": func(e *domain.Event) {
				e.Month = "2026-13"
			},
			"a month without its leading zero": func(e *domain.Event) {
				// "2026-7" would sort after "2026-12", so the log's months
				// would come back in an order that is not chronological.
				e.Month = "2026-7"
			},
			"a month with a day on it": func(e *domain.Event) {
				e.Month = "2026-07-12"
			},
			"a year on its own": func(e *domain.Event) {
				e.Month = "2026"
			},
			"an empty type": func(e *domain.Event) {
				e.Type = ""
			},
			"a type that is only whitespace": func(e *domain.Event) {
				// Normalize trims it to empty; Validate is what refuses it.
				*e = domain.Event{
					Action: e.Action, Month: e.Month, Type: "   ",
					Amount: e.Amount, Direction: e.Direction, RecordedAt: e.RecordedAt,
				}
				*e = e.Normalize()
			},
			"a direction that is neither": func(e *domain.Event) {
				e.Direction = "refund"
			},
			"an unset direction, which Normalize should have filled": func(e *domain.Event) {
				e.Direction = ""
			},
			"an unset recordedAt, which the log cannot order": func(e *domain.Event) {
				e.RecordedAt = time.Time{}
			},
		}

		for name, mutate := range tests {
			t.Run(name, func(t *testing.T) {
				e := valid()
				mutate(&e)

				err := e.Validate()
				if err == nil {
					t.Fatalf("Validate() accepted %+v; an append-only log cannot take this back", e)
				}
				if !errors.Is(err, domain.ErrInvalidEvent) {
					t.Errorf("Validate() gave %v, which does not match ErrInvalidEvent; callers tell a bad submission from a broken database that way", err)
				}
			})
		}
	})
}
