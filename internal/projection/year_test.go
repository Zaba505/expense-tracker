package projection_test

import (
	"errors"
	"testing"

	"github.com/Zaba505/expense-tracker/internal/money"
	"github.com/Zaba505/expense-tracker/internal/projection"
)

func TestProjectYear(t *testing.T) {
	t.Run("renders a year's months, types, totals, and averages from state", func(t *testing.T) {
		state := projection.State{
			expense("2025-12", "Mortgage"):  999_999,
			expense("2026-01", "Rent"):      120_000,
			expense("2026-01", "Travel"):    24_000,
			income("2026-01", "Paycheck"):   300_000,
			expense("2026-02", "Rent"):      120_000,
			expense("2026-02", "Groceries"): 12_000,
			income("2026-02", "Paycheck"):   300_000,
			expense("2027-01", "Mortgage"):  888_888,
		}

		got, err := projection.ProjectYear(state, "2026")
		if err != nil {
			t.Fatalf("ProjectYear() error = %v", err)
		}

		if got.Year != "2026" {
			t.Fatalf("ProjectYear().Year = %q, want %q", got.Year, "2026")
		}

		wantTypes := []string{"Groceries", "Paycheck", "Rent", "Travel"}
		if len(got.Types) != len(wantTypes) {
			t.Fatalf("ProjectYear().Types = %#v, want %#v", got.Types, wantTypes)
		}
		for i := range wantTypes {
			if got.Types[i] != wantTypes[i] {
				t.Errorf("ProjectYear().Types[%d] = %q, want %q", i, got.Types[i], wantTypes[i])
			}
		}

		if len(got.Months) != 12 {
			t.Fatalf("len(ProjectYear().Months) = %d, want 12", len(got.Months))
		}

		assertYearMonth(t, got.Months[0], "2026-01", map[string]money.Cents{
			"Paycheck":  300_000,
			"Rent":      120_000,
			"Travel":    24_000,
			"Groceries": 0,
		}, projection.Rollup{Expenses: 144_000, Income: 300_000})
		assertYearMonth(t, got.Months[1], "2026-02", map[string]money.Cents{
			"Paycheck":  300_000,
			"Rent":      120_000,
			"Travel":    0,
			"Groceries": 12_000,
		}, projection.Rollup{Expenses: 132_000, Income: 300_000})
		assertYearMonth(t, got.Months[2], "2026-03", map[string]money.Cents{
			"Paycheck":  0,
			"Rent":      0,
			"Travel":    0,
			"Groceries": 0,
		}, projection.Rollup{})

		assertSummary(t, got.Total, map[string]money.Cents{
			"Paycheck":  600_000,
			"Rent":      240_000,
			"Travel":    24_000,
			"Groceries": 12_000,
		}, projection.Rollup{Expenses: 276_000, Income: 600_000})
		assertSummary(t, got.Average, map[string]money.Cents{
			"Paycheck":  50_000,
			"Rent":      20_000,
			"Travel":    2_000,
			"Groceries": 1_000,
		}, projection.Rollup{Expenses: 23_000, Income: 50_000})
	})

	t.Run("refuses an invalid year", func(t *testing.T) {
		_, err := projection.ProjectYear(projection.State{}, "2026-07")
		if !errors.Is(err, projection.ErrInvalidYear) {
			t.Fatalf("ProjectYear() error = %v, want ErrInvalidYear", err)
		}
	})

	t.Run("refuses a direction rollups cannot count", func(t *testing.T) {
		state := projection.State{
			{Month: "2026-01", Type: "Mystery", Direction: "transfer"}: 12_34,
		}

		_, err := projection.ProjectYear(state, "2026")
		if !errors.Is(err, projection.ErrUnknownDirection) {
			t.Fatalf("ProjectYear() error = %v, want ErrUnknownDirection", err)
		}
	})
}

func assertYearMonth(t *testing.T, got projection.YearMonth, wantMonth string, wantAmounts map[string]money.Cents, wantRollup projection.Rollup) {
	t.Helper()

	if got.Month != wantMonth {
		t.Fatalf("YearMonth.Month = %q, want %q", got.Month, wantMonth)
	}
	for typ, want := range wantAmounts {
		if amount := got.Amount(typ); amount != want {
			t.Errorf("YearMonth.Amount(%q) = %s, want %s", typ, amount, want)
		}
	}
	if got.Rollup != wantRollup {
		t.Errorf("YearMonth.Rollup = %+v, want %+v", got.Rollup, wantRollup)
	}
}

func assertSummary(t *testing.T, got projection.YearSummary, wantAmounts map[string]money.Cents, wantRollup projection.Rollup) {
	t.Helper()

	for typ, want := range wantAmounts {
		if amount := got.Amount(typ); amount != want {
			t.Errorf("YearSummary.Amount(%q) = %s, want %s", typ, amount, want)
		}
	}
	if got.Rollup != wantRollup {
		t.Errorf("YearSummary.Rollup = %+v, want %+v", got.Rollup, wantRollup)
	}
}
