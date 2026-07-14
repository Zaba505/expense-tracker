package projection_test

import (
	"testing"
	"time"

	"github.com/Zaba505/expense-tracker/internal/domain"
	"github.com/Zaba505/expense-tracker/internal/projection"
)

func TestPreviewTypeRename(t *testing.T) {
	t.Run("shows affected months and entry counts for the current canonical type", func(t *testing.T) {
		got, err := projection.PreviewTypeRename([]domain.Event{
			eventAt(domain.ActionAdd, "2026-01", "Fuel", 8_400, time.Date(2026, 1, 10, 9, 0, 0, 0, time.UTC)),
			eventAt(domain.ActionAdd, "2026-01", "Fuel", 1_600, time.Date(2026, 1, 11, 9, 0, 0, 0, time.UTC)),
			eventAt(domain.ActionAdd, "2026-02", "Fuel", 9_100, time.Date(2026, 2, 10, 9, 0, 0, 0, time.UTC)),
			eventAt(domain.ActionAdd, "2026-03", "Gas", 9_200, time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC)),
		}, "Fuel", "Gas")
		if err != nil {
			t.Fatalf("PreviewTypeRename() error = %v", err)
		}

		if got.FromType != "Fuel" || got.ToType != "Gas" {
			t.Fatalf("PreviewTypeRename() = %+v, want Fuel -> Gas", got)
		}
		if got.AffectedEntries != 3 {
			t.Fatalf("PreviewTypeRename().AffectedEntries = %d, want 3", got.AffectedEntries)
		}

		want := []projection.TypeRenameMonth{
			{Month: "2026-01", Entries: 2},
			{Month: "2026-02", Entries: 1},
		}
		if len(got.Months) != len(want) {
			t.Fatalf("PreviewTypeRename().Months = %+v, want %+v", got.Months, want)
		}
		for i := range want {
			if got.Months[i] != want[i] {
				t.Errorf("PreviewTypeRename().Months[%d] = %+v, want %+v", i, got.Months[i], want[i])
			}
		}
	})

	t.Run("treats previously renamed history as already canonicalized", func(t *testing.T) {
		got, err := projection.PreviewTypeRename([]domain.Event{
			eventAt(domain.ActionAdd, "2026-01", "Fuel", 8_400, time.Date(2026, 1, 10, 9, 0, 0, 0, time.UTC)),
			{
				Action:     domain.ActionRenameType,
				Month:      "2026-06",
				Type:       "Fuel",
				ToType:     "Gas",
				Direction:  domain.DirectionExpense,
				RecordedAt: time.Date(2026, 6, 10, 9, 0, 0, 0, time.UTC),
			},
		}, "Fuel", "Gas")
		if err != nil {
			t.Fatalf("PreviewTypeRename() error = %v", err)
		}

		if got.FromType != "Gas" || got.ToType != "Gas" {
			t.Fatalf("PreviewTypeRename() = %+v, want canonical Gas -> Gas", got)
		}
		if got.AffectedEntries != 0 || len(got.Months) != 0 {
			t.Errorf("PreviewTypeRename() = %+v, want no remaining impact after the earlier rename", got)
		}
	})
}
