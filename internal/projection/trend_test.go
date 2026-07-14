package projection_test

import (
	"errors"
	"testing"
	"time"

	"github.com/Zaba505/expense-tracker/internal/domain"
	"github.com/Zaba505/expense-tracker/internal/money"
	"github.com/Zaba505/expense-tracker/internal/projection"
)

func TestProjectTrend(t *testing.T) {
	t.Run("spans the type's range, with the months it lapsed as gaps", func(t *testing.T) {
		// Gas runs 2025-12 to 2026-03 and lapses in between. The log is wider on
		// both sides — Rent in 2025-11, Paycheck in 2026-04 — and that is not
		// Gas's business: the report starts and ends where Gas does.
		state := projection.State{
			expense("2025-11", "Rent"):    120_000,
			expense("2025-12", "Gas"):     4_000,
			expense("2026-01", "Rent"):    120_000,
			expense("2026-02", "Gas"):     6_000,
			expense("2026-03", "Gas"):     5_000,
			income("2026-04", "Paycheck"): 300_000,
		}

		got, err := projection.ProjectTrend(state, "Gas")
		if err != nil {
			t.Fatalf("ProjectTrend() error = %v", err)
		}

		if got.Type != "Gas" {
			t.Errorf("ProjectTrend().Type = %q, want %q", got.Type, "Gas")
		}

		// January is a gap in the middle of Gas's life. November and April are
		// not rows at all — Gas has nothing to say about them.
		assertTrendMonths(t, got, []projection.TrendMonth{
			{Month: "2025-12", Amount: 4_000, Recorded: true},
			{Month: "2026-01", Amount: 0, Recorded: false},
			{Month: "2026-02", Amount: 6_000, Recorded: true},
			{Month: "2026-03", Amount: 5_000, Recorded: true},
		})
	})

	t.Run("is not stretched by another type's months", func(t *testing.T) {
		// The range comes from the type, not from every key in State. This is
		// what keeps one mistyped month — the year segment of the month input
		// slipped, giving a well-formed month domain.ValidMonth accepts — from
		// putting twenty thousand rows on every other type's trend. State is the
		// fold of an append-only log, so that key would never leave it.
		state := projection.State{
			expense("0216-01", "Groceries"): 12_000, // the slip: 2026 typed as 0216
			expense("2026-01", "Gas"):       4_000,
			expense("2026-02", "Gas"):       6_000,
		}

		got, err := projection.ProjectTrend(state, "Gas")
		if err != nil {
			t.Fatalf("ProjectTrend() error = %v", err)
		}

		if len(got.Months) != 2 {
			t.Fatalf("len(ProjectTrend().Months) = %d, want 2: another type's mistyped month stretched this trend", len(got.Months))
		}
	})

	t.Run("heals when a mistyped month is voided", func(t *testing.T) {
		// And the trend of the type that owns the mistyped month is distorted —
		// but only until the owner voids it, which is the correction they would
		// reach for anyway. A void is a compensating add (view.VoidAmount), so
		// the cell survives at zero; a month that comes to nothing is not a
		// recorded month, so it drops out of the range and the report is whole.
		voided, err := projection.Fold([]domain.Event{
			event(domain.ActionAdd, "0216-01", "Groceries", 12_000),
			event(domain.ActionAdd, "0216-01", "Groceries", -12_000), // the void
			event(domain.ActionAdd, "2026-01", "Groceries", 15_000),
			event(domain.ActionAdd, "2026-02", "Groceries", 13_000),
		})
		if err != nil {
			t.Fatalf("Fold() error = %v", err)
		}

		got, err := projection.ProjectTrend(voided, "Groceries")
		if err != nil {
			t.Fatalf("ProjectTrend() error = %v", err)
		}

		assertTrendMonths(t, got, []projection.TrendMonth{
			{Month: "2026-01", Amount: 15_000, Recorded: true},
			{Month: "2026-02", Amount: 13_000, Recorded: true},
		})
	})

	t.Run("summarizes the recorded months and not the gaps", func(t *testing.T) {
		state := projection.State{
			expense("2026-01", "Gas"):  4_000,
			expense("2026-02", "Rent"): 120_000, // a gap for Gas
			expense("2026-03", "Gas"):  6_000,
			expense("2026-04", "Gas"):  5_000,
		}

		got, err := projection.ProjectTrend(state, "Gas")
		if err != nil {
			t.Fatalf("ProjectTrend() error = %v", err)
		}

		// Four months in the range, three of them recorded. The average is over
		// the three: 150.00, not the 112.50 that averaging the gap in as a zero
		// would give.
		if len(got.Months) != 4 {
			t.Errorf("len(ProjectTrend().Months) = %d, want 4", len(got.Months))
		}
		assertTrendSummary(t, got, projection.Trend{
			Observed: 3,
			Min:      4_000,
			Max:      6_000,
			Total:    15_000,
			Average:  5_000,
		})
	})

	t.Run("counts a month that comes to nothing as a gap, not an observed zero", func(t *testing.T) {
		// 2026-02 is a cell that nets to zero. The log cannot say whether that
		// was meant as "nothing was spent" or is the residue of a void — a void
		// is a compensating add, so it leaves exactly this — and the trend does
		// not pretend to know. Either way the type came to nothing, which is
		// what a gap says.
		//
		// The alternative, counting it as an observed $0.00, is what makes the
		// void that corrects a mistake into a month of history: it would pull
		// Min down to $0.00 and divide the average by a month Gas was never
		// spent in.
		state := projection.State{
			expense("2026-01", "Gas"):  4_000,
			expense("2026-02", "Gas"):  0,
			expense("2026-03", "Gas"):  6_000,
			expense("2026-04", "Rent"): 120_000,
		}

		got, err := projection.ProjectTrend(state, "Gas")
		if err != nil {
			t.Fatalf("ProjectTrend() error = %v", err)
		}

		assertTrendMonths(t, got, []projection.TrendMonth{
			{Month: "2026-01", Amount: 4_000, Recorded: true},
			{Month: "2026-02", Amount: 0, Recorded: false},
			{Month: "2026-03", Amount: 6_000, Recorded: true},
		})

		if !got.Months[1].Gap() {
			t.Error("ProjectTrend().Months[1].Gap() = false, want true: a month that comes to nothing is a gap")
		}

		// The minimum is the $40.00 Gas actually cost, not the $0.00 of the
		// month it did not, and the average is over the two months it did.
		assertTrendSummary(t, got, projection.Trend{
			Observed: 2,
			Min:      4_000,
			Max:      6_000,
			Total:    10_000,
			Average:  5_000,
		})
	})

	t.Run("does not let a voided entry into the summaries", func(t *testing.T) {
		// The month view retires an entry by appending the opposite of it, since
		// an append-only log cannot delete one. February's cell therefore
		// survives at zero, and a trend that read it as a $50.00-type month it
		// happened to spend nothing in would report a minimum of $0.00 and an
		// average halved by a month that never happened.
		state, err := projection.Fold([]domain.Event{
			event(domain.ActionAdd, "2026-01", "Groceries", 10_000),
			event(domain.ActionAdd, "2026-02", "Groceries", 5_000),
			event(domain.ActionAdd, "2026-02", "Groceries", -5_000), // the void
			event(domain.ActionAdd, "2026-03", "Groceries", 12_000),
		})
		if err != nil {
			t.Fatalf("Fold() error = %v", err)
		}

		got, err := projection.ProjectTrend(state, "Groceries")
		if err != nil {
			t.Fatalf("ProjectTrend() error = %v", err)
		}

		if !got.Months[1].Gap() {
			t.Error("the voided month is not a gap: the correction became a month of history")
		}
		assertTrendSummary(t, got, projection.Trend{
			Observed: 2,
			Min:      10_000,
			Max:      12_000,
			Total:    22_000,
			Average:  11_000,
		})
	})

	t.Run("crosses the year boundary", func(t *testing.T) {
		state := projection.State{
			expense("2025-11", "Gas"): 4_000,
			expense("2026-02", "Gas"): 6_000,
		}

		got, err := projection.ProjectTrend(state, "Gas")
		if err != nil {
			t.Fatalf("ProjectTrend() error = %v", err)
		}

		// December's successor is January, not a thirteenth month of 2025.
		assertTrendMonths(t, got, []projection.TrendMonth{
			{Month: "2025-11", Amount: 4_000, Recorded: true},
			{Month: "2025-12", Amount: 0, Recorded: false},
			{Month: "2026-01", Amount: 0, Recorded: false},
			{Month: "2026-02", Amount: 6_000, Recorded: true},
		})
	})

	t.Run("sums a type recorded in both directions", func(t *testing.T) {
		// A cell is per-direction (see projection.Key), but a trend is of the
		// type, so the two cells of one month are one row.
		state := projection.State{
			expense("2026-01", "Refunds"): 3_000,
			income("2026-01", "Refunds"):  1_000,
		}

		got, err := projection.ProjectTrend(state, "Refunds")
		if err != nil {
			t.Fatalf("ProjectTrend() error = %v", err)
		}

		assertTrendMonths(t, got, []projection.TrendMonth{
			{Month: "2026-01", Amount: 4_000, Recorded: true},
		})
	})

	t.Run("reads a renamed type's history under its new name", func(t *testing.T) {
		// The rename is not this projection's business — Fold canonicalizes it
		// away — but that it reaches the trend is worth pinning down, because a
		// trend that split one type's history in two at the rename would be the
		// one report where a rename is visible as a discontinuity.
		state, err := projection.Fold([]domain.Event{
			event(domain.ActionSet, "2026-01", "Fuel", 4_000),
			event(domain.ActionSet, "2026-02", "Fuel", 6_000),
			renameAt("Fuel", "Gas", time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)),
			event(domain.ActionSet, "2026-03", "Gas", 5_000),
		})
		if err != nil {
			t.Fatalf("Fold() error = %v", err)
		}

		gas, err := projection.ProjectTrend(state, "Gas")
		if err != nil {
			t.Fatalf("ProjectTrend() error = %v", err)
		}

		// Gas reaches back through the months that were recorded as Fuel.
		assertTrendMonths(t, gas, []projection.TrendMonth{
			{Month: "2026-01", Amount: 4_000, Recorded: true},
			{Month: "2026-02", Amount: 6_000, Recorded: true},
			{Month: "2026-03", Amount: 5_000, Recorded: true},
		})

		// And Fuel is a name the log no longer reads anything under.
		fuel, err := projection.ProjectTrend(state, "Fuel")
		if err != nil {
			t.Fatalf("ProjectTrend() error = %v", err)
		}
		if !fuel.Empty() {
			t.Errorf("ProjectTrend(%q).Empty() = false, want true: the rename took the name's history with it", "Fuel")
		}
	})

	t.Run("is empty for a type the log never mentioned", func(t *testing.T) {
		state := projection.State{
			expense("2026-01", "Rent"): 120_000,
		}

		got, err := projection.ProjectTrend(state, "Yacht")
		if err != nil {
			t.Fatalf("ProjectTrend() error = %v", err)
		}

		if !got.Empty() {
			t.Errorf("ProjectTrend().Empty() = false, want true")
		}
		// A type with nothing behind it has no range of its own, so there are no
		// rows to render — the page says the log is silent rather than printing
		// the log's months back as a wall of dashes.
		if len(got.Months) != 0 {
			t.Errorf("len(ProjectTrend().Months) = %d, want 0", len(got.Months))
		}
		assertTrendSummary(t, got, projection.Trend{})
	})

	t.Run("is empty for an empty log", func(t *testing.T) {
		got, err := projection.ProjectTrend(projection.State{}, "Gas")
		if err != nil {
			t.Fatalf("ProjectTrend() error = %v", err)
		}

		if !got.Empty() {
			t.Errorf("ProjectTrend().Empty() = false, want true")
		}
		if len(got.Months) != 0 {
			t.Errorf("len(ProjectTrend().Months) = %d, want 0: an empty log has no range", len(got.Months))
		}
		if got.Type != "Gas" {
			t.Errorf("ProjectTrend().Type = %q, want %q", got.Type, "Gas")
		}
	})

	t.Run("summarizes a type that only ever cost money without a phantom zero", func(t *testing.T) {
		// The minimum is seeded from the first recorded month, not from the zero
		// value, so a type recorded in one month of a five-month log reports what
		// it cost rather than $0.00.
		state := projection.State{
			expense("2026-01", "Rent"):   120_000,
			expense("2026-03", "Travel"): 24_000,
			expense("2026-05", "Rent"):   120_000,
		}

		got, err := projection.ProjectTrend(state, "Travel")
		if err != nil {
			t.Fatalf("ProjectTrend() error = %v", err)
		}

		assertTrendSummary(t, got, projection.Trend{
			Observed: 1,
			Min:      24_000,
			Max:      24_000,
			Total:    24_000,
			Average:  24_000,
		})
	})

	t.Run("keeps a walked-back month negative", func(t *testing.T) {
		// A negative cell is a correction of an overstatement, and it is the
		// minimum of the months it sits among rather than being clamped away.
		state := projection.State{
			expense("2026-01", "Gas"): 4_000,
			expense("2026-02", "Gas"): -1_000,
		}

		got, err := projection.ProjectTrend(state, "Gas")
		if err != nil {
			t.Fatalf("ProjectTrend() error = %v", err)
		}

		assertTrendSummary(t, got, projection.Trend{
			Observed: 2,
			Min:      -1_000,
			Max:      4_000,
			Total:    3_000,
			Average:  1_500,
		})
	})

	t.Run("trims the type it is asked for", func(t *testing.T) {
		// Types are stored trimmed (domain.Event.Normalize), so a type arriving
		// from a query string with a stray space is the same type.
		state := projection.State{
			expense("2026-01", "Gas"): 4_000,
		}

		got, err := projection.ProjectTrend(state, "  Gas  ")
		if err != nil {
			t.Fatalf("ProjectTrend() error = %v", err)
		}

		if got.Type != "Gas" {
			t.Errorf("ProjectTrend().Type = %q, want %q", got.Type, "Gas")
		}
		if got.Empty() {
			t.Error("ProjectTrend().Empty() = true, want false: a padded type is the same type")
		}
	})

	t.Run("refuses a trend of no type", func(t *testing.T) {
		for _, typ := range []string{"", "   "} {
			_, err := projection.ProjectTrend(projection.State{}, typ)
			if !errors.Is(err, projection.ErrInvalidType) {
				t.Errorf("ProjectTrend(%q) error = %v, want ErrInvalidType", typ, err)
			}
		}
	})

	t.Run("refuses a direction the rollups cannot count", func(t *testing.T) {
		state := projection.State{
			{Month: "2026-01", Type: "Mystery", Direction: "transfer"}: 12_34,
		}

		_, err := projection.ProjectTrend(state, "Mystery")
		if !errors.Is(err, projection.ErrUnknownDirection) {
			t.Fatalf("ProjectTrend() error = %v, want ErrUnknownDirection", err)
		}
	})

	t.Run("refuses a bad direction on a type it was not asked about", func(t *testing.T) {
		// The guard runs over every cell, not only the requested type's. A State
		// the yearly grid refuses to read is one the trend refuses to read too:
		// two reports disagreeing about whether the log can be read at all is
		// worse than either answer.
		state := projection.State{
			expense("2026-01", "Gas"):                                  4_000,
			{Month: "2026-01", Type: "Mystery", Direction: "transfer"}: 12_34,
		}

		_, err := projection.ProjectTrend(state, "Gas")
		if !errors.Is(err, projection.ErrUnknownDirection) {
			t.Fatalf("ProjectTrend() error = %v, want ErrUnknownDirection", err)
		}
	})
}

func assertTrendMonths(t *testing.T, got projection.Trend, want []projection.TrendMonth) {
	t.Helper()

	if len(got.Months) != len(want) {
		t.Fatalf("ProjectTrend().Months = %+v (%d months), want %+v (%d months)", got.Months, len(got.Months), want, len(want))
	}
	for i := range want {
		if got.Months[i] != want[i] {
			t.Errorf("ProjectTrend().Months[%d] = %+v, want %+v", i, got.Months[i], want[i])
		}
	}
}

// assertTrendSummary compares only the summary fields of a trend: Observed and
// the money the recorded months came to.
func assertTrendSummary(t *testing.T, got projection.Trend, want projection.Trend) {
	t.Helper()

	if got.Observed != want.Observed {
		t.Errorf("ProjectTrend().Observed = %d, want %d", got.Observed, want.Observed)
	}
	for _, field := range []struct {
		name      string
		got, want money.Cents
	}{
		{"Min", got.Min, want.Min},
		{"Max", got.Max, want.Max},
		{"Total", got.Total, want.Total},
		{"Average", got.Average, want.Average},
	} {
		if field.got != field.want {
			t.Errorf("ProjectTrend().%s = %s, want %s", field.name, field.got, field.want)
		}
	}
}
