package projection_test

import (
	"testing"
	"time"

	"github.com/Zaba505/expense-tracker/internal/domain"
	"github.com/Zaba505/expense-tracker/internal/money"
	"github.com/Zaba505/expense-tracker/internal/projection"
)

func TestPreviewTypeRename(t *testing.T) {
	t.Run("reports the cells the rename would move, and what each would come to", func(t *testing.T) {
		got := preview(t, []domain.Event{
			eventAt(domain.ActionAdd, "2026-01", "Fuel", 8_400, time.Date(2026, 1, 10, 9, 0, 0, 0, time.UTC)),
			eventAt(domain.ActionAdd, "2026-01", "Fuel", 1_600, time.Date(2026, 1, 11, 9, 0, 0, 0, time.UTC)),
			eventAt(domain.ActionAdd, "2026-02", "Fuel", 9_100, time.Date(2026, 2, 10, 9, 0, 0, 0, time.UTC)),
			eventAt(domain.ActionAdd, "2026-02", "Gas", 900, time.Date(2026, 2, 11, 9, 0, 0, 0, time.UTC)),
			eventAt(domain.ActionAdd, "2026-03", "Gas", 9_200, time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC)),
		}, "Fuel", "Gas")

		if got.FromType != "Fuel" || got.ToType != "Gas" {
			t.Fatalf("PreviewTypeRename() = %+v, want Fuel -> Gas", got)
		}
		if got.AffectedEntries != 3 {
			t.Errorf("PreviewTypeRename().AffectedEntries = %d, want 3", got.AffectedEntries)
		}
		if got.Months() != 2 {
			t.Errorf("PreviewTypeRename().Months() = %d, want 2 — March is Gas already", got.Months())
		}
		if got.Conflicts {
			t.Error("PreviewTypeRename().Conflicts = true, want false — adds merge by summing")
		}

		// March holds no Fuel, so the rename does not touch it and it is not in
		// the list. February is the merge: $91.00 of Fuel onto $9.00 of Gas.
		assertCells(t, got.Cells, []projection.TypeRenameCell{
			{
				Month: "2026-01", Direction: domain.DirectionExpense, Entries: 2,
				From: money.Cents(10_000), To: money.Cents(0), Result: money.Cents(10_000),
			},
			{
				Month: "2026-02", Direction: domain.DirectionExpense, Entries: 1,
				From: money.Cents(9_100), To: money.Cents(900), Result: money.Cents(10_000),
			},
		})
	})

	t.Run("merging two set totals in one cell is a conflict, because one would be dropped", func(t *testing.T) {
		// This is the case the preview exists for. Both events are true — the
		// sheet said Fuel was $40.00 and Gas was $10.00 — but a set supersedes
		// rather than sums, so folding them under one type keeps only the later
		// one, and $40.00 the owner recorded leaves the month.
		got := preview(t, []domain.Event{
			eventAt(domain.ActionSet, "2026-01", "Fuel", 40_00, time.Date(2026, 1, 10, 9, 0, 0, 0, time.UTC)),
			eventAt(domain.ActionSet, "2026-01", "Gas", 10_00, time.Date(2026, 1, 11, 9, 0, 0, 0, time.UTC)),
		}, "Fuel", "Gas")

		if !got.Conflicts {
			t.Fatal("PreviewTypeRename().Conflicts = false, want true — the merge would drop $40.00")
		}
		assertCells(t, got.Cells, []projection.TypeRenameCell{
			{
				Month: "2026-01", Direction: domain.DirectionExpense, Entries: 1,
				From: money.Cents(40_00), To: money.Cents(10_00), Result: money.Cents(10_00),
			},
		})
	})

	t.Run("a set on the target before the source's adds is not a conflict", func(t *testing.T) {
		// Order decides it: the set lands first and the adds pile onto it, so
		// nothing is superseded and no money leaves the month.
		got := preview(t, []domain.Event{
			eventAt(domain.ActionSet, "2026-01", "Gas", 10_00, time.Date(2026, 1, 10, 9, 0, 0, 0, time.UTC)),
			eventAt(domain.ActionAdd, "2026-01", "Fuel", 40_00, time.Date(2026, 1, 11, 9, 0, 0, 0, time.UTC)),
		}, "Fuel", "Gas")

		if got.Conflicts {
			t.Error("PreviewTypeRename().Conflicts = true, want false — the adds land on top of the set")
		}
		assertCells(t, got.Cells, []projection.TypeRenameCell{
			{
				Month: "2026-01", Direction: domain.DirectionExpense, Entries: 1,
				From: money.Cents(40_00), To: money.Cents(10_00), Result: money.Cents(50_00),
			},
		})
	})

	t.Run("expense and income are separate cells, because money never crosses between them", func(t *testing.T) {
		got := preview(t, []domain.Event{
			eventAt(domain.ActionAdd, "2026-01", "Fuel", 40_00, time.Date(2026, 1, 10, 9, 0, 0, 0, time.UTC)),
			asIncome(eventAt(domain.ActionAdd, "2026-01", "Fuel", 5_00, time.Date(2026, 1, 11, 9, 0, 0, 0, time.UTC))),
		}, "Fuel", "Gas")

		assertCells(t, got.Cells, []projection.TypeRenameCell{
			{
				Month: "2026-01", Direction: domain.DirectionExpense, Entries: 1,
				From: money.Cents(40_00), To: money.Cents(0), Result: money.Cents(40_00),
			},
			{
				Month: "2026-01", Direction: domain.DirectionIncome, Entries: 1,
				From: money.Cents(5_00), To: money.Cents(0), Result: money.Cents(5_00),
			},
		})
	})

	t.Run("history already renamed away has nothing left to rename", func(t *testing.T) {
		got := preview(t, []domain.Event{
			eventAt(domain.ActionAdd, "2026-01", "Fuel", 8_400, time.Date(2026, 1, 10, 9, 0, 0, 0, time.UTC)),
			renameAt("Fuel", "Gas", time.Date(2026, 6, 10, 9, 0, 0, 0, time.UTC)),
		}, "Fuel", "Gas")

		if got.AffectedEntries != 0 || len(got.Cells) != 0 {
			t.Errorf("PreviewTypeRename() = %+v, want no impact left after the earlier rename", got)
		}
	})

	t.Run("renaming a type to itself does nothing", func(t *testing.T) {
		got := preview(t, []domain.Event{
			eventAt(domain.ActionAdd, "2026-01", "Fuel", 8_400, time.Date(2026, 1, 10, 9, 0, 0, 0, time.UTC)),
		}, "Fuel", "Fuel")

		if got.AffectedEntries != 0 || len(got.Cells) != 0 || got.Conflicts {
			t.Errorf("PreviewTypeRename() = %+v, want no impact", got)
		}
	})
}

func preview(t *testing.T, events []domain.Event, from, to string) projection.TypeRenamePreview {
	t.Helper()

	got, err := projection.PreviewTypeRename(events, from, to)
	if err != nil {
		t.Fatalf("PreviewTypeRename() error = %v", err)
	}
	return got
}

func asIncome(e domain.Event) domain.Event {
	e.Direction = domain.DirectionIncome
	return e
}

func assertCells(t *testing.T, got, want []projection.TypeRenameCell) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("PreviewTypeRename().Cells = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("PreviewTypeRename().Cells[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}
