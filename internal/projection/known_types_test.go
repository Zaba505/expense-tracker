package projection_test

import (
	"testing"
	"time"

	"github.com/Zaba505/expense-tracker/internal/domain"
	"github.com/Zaba505/expense-tracker/internal/money"
	"github.com/Zaba505/expense-tracker/internal/projection"
)

func TestKnownTypes(t *testing.T) {
	t.Run("returns distinct types with their last used month, newest first", func(t *testing.T) {
		got := projection.KnownTypes([]domain.Event{
			eventAt(domain.ActionSet, "2026-01", "Rent", 180_000, time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)),
			eventAt(domain.ActionAdd, "2026-02", "Groceries", 8_400, time.Date(2026, 2, 5, 9, 0, 0, 0, time.UTC)),
			eventAt(domain.ActionAdd, "2026-03", "Groceries", 9_100, time.Date(2026, 3, 5, 9, 0, 0, 0, time.UTC)),
			eventAt(domain.ActionSet, "2026-04", "Mortgage", 210_000, time.Date(2026, 4, 5, 9, 0, 0, 0, time.UTC)),
			eventAt(domain.ActionAdd, "2026-05", "Rent", -45_000, time.Date(2026, 5, 5, 9, 0, 0, 0, time.UTC)),
		})

		want := []projection.KnownType{
			{Type: "Rent", LastUsedMonth: "2026-05"},
			{Type: "Mortgage", LastUsedMonth: "2026-04"},
			{Type: "Groceries", LastUsedMonth: "2026-03"},
		}
		assertKnownTypes(t, got, want)
	})

	t.Run("includes types retired months ago", func(t *testing.T) {
		got := projection.KnownTypes([]domain.Event{
			eventAt(domain.ActionSet, "2025-11", "Renters Insurance", 12_000, time.Date(2025, 11, 2, 9, 0, 0, 0, time.UTC)),
			eventAt(domain.ActionSet, "2026-05", "Mortgage", 210_000, time.Date(2026, 5, 2, 9, 0, 0, 0, time.UTC)),
			eventAt(domain.ActionAdd, "2026-06", "Groceries", 8_400, time.Date(2026, 6, 2, 9, 0, 0, 0, time.UTC)),
		})

		want := []projection.KnownType{
			{Type: "Groceries", LastUsedMonth: "2026-06"},
			{Type: "Mortgage", LastUsedMonth: "2026-05"},
			{Type: "Renters Insurance", LastUsedMonth: "2025-11"},
		}
		assertKnownTypes(t, got, want)
	})

	t.Run("a new type appears immediately after its first event, even for an older month", func(t *testing.T) {
		got := projection.KnownTypes([]domain.Event{
			eventAt(domain.ActionAdd, "2026-07", "Groceries", 8_400, time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC)),
			eventAt(domain.ActionSet, "2026-06", "Rent", 180_000, time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)),
			eventAt(domain.ActionAdd, "2024-12", "Travel", 50_000, time.Date(2026, 7, 12, 9, 0, 0, 0, time.UTC)),
		})

		want := []projection.KnownType{
			{Type: "Travel", LastUsedMonth: "2024-12"},
			{Type: "Rent", LastUsedMonth: "2026-06"},
			{Type: "Groceries", LastUsedMonth: "2026-07"},
		}
		assertKnownTypes(t, got, want)
	})

	t.Run("normalizes types before deduplicating them", func(t *testing.T) {
		got := projection.KnownTypes([]domain.Event{
			eventAt(domain.ActionAdd, "2026-07", "Groceries", 8_400, time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC)),
			eventAt(domain.ActionAdd, "2026-08", "  Groceries  ", 9_100, time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)),
		})

		want := []projection.KnownType{
			{Type: "Groceries", LastUsedMonth: "2026-08"},
		}
		assertKnownTypes(t, got, want)
	})
}

func eventAt(action domain.Action, month, typ string, amount int64, recordedAt time.Time) domain.Event {
	e := event(action, month, typ, money.Cents(amount))
	e.RecordedAt = recordedAt
	return e
}

func assertKnownTypes(t *testing.T, got, want []projection.KnownType) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("KnownTypes() = %#v (%d types), want %#v (%d types)", got, len(got), want, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("KnownTypes()[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}
