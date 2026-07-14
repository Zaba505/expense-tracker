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
	t.Run("spans the log's full range, gaps included", func(t *testing.T) {
		// The log runs 2025-11 to 2026-03; Gas is only in three of those five
		// months, and is absent from the first and the last.
		state := projection.State{
			expense("2025-11", "Rent"):    120_000,
			expense("2025-12", "Gas"):     4_000,
			expense("2026-01", "Gas"):     6_000,
			expense("2026-02", "Gas"):     5_000,
			expense("2026-03", "Rent"):    120_000,
			income("2026-03", "Paycheck"): 300_000,
		}

		got, err := projection.ProjectTrend(state, "Gas")
		if err != nil {
			t.Fatalf("ProjectTrend() error = %v", err)
		}

		if got.Type != "Gas" {
			t.Errorf("ProjectTrend().Type = %q, want %q", got.Type, "Gas")
		}

		// Every month of the log, not only the months Gas appears in: the gap
		// before its first use and the gap after its last are the point.
		assertTrendMonths(t, got, []projection.TrendMonth{
			{Month: "2025-11", Amount: 0, Recorded: false},
			{Month: "2025-12", Amount: 4_000, Recorded: true},
			{Month: "2026-01", Amount: 6_000, Recorded: true},
			{Month: "2026-02", Amount: 5_000, Recorded: true},
			{Month: "2026-03", Amount: 0, Recorded: false},
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

	t.Run("tells a recorded zero from a gap", func(t *testing.T) {
		// The distinction the whole report turns on. 2026-02 is a cell an event
		// set to nothing — how an entry is retired in a log that cannot delete
		// one — and 2026-03 is a month the log never mentioned Gas in at all.
		// They are both $0.00 to a naive read of the state, and they mean
		// opposite things.
		state := projection.State{
			expense("2026-01", "Gas"):  4_000,
			expense("2026-02", "Gas"):  0,
			expense("2026-03", "Rent"): 120_000,
		}

		got, err := projection.ProjectTrend(state, "Gas")
		if err != nil {
			t.Fatalf("ProjectTrend() error = %v", err)
		}

		assertTrendMonths(t, got, []projection.TrendMonth{
			{Month: "2026-01", Amount: 4_000, Recorded: true},
			{Month: "2026-02", Amount: 0, Recorded: true},
			{Month: "2026-03", Amount: 0, Recorded: false},
		})

		if got.Months[1].Gap() {
			t.Error("ProjectTrend().Months[1].Gap() = true, want false: a zero an event set is not a gap")
		}
		if !got.Months[2].Gap() {
			t.Error("ProjectTrend().Months[2].Gap() = false, want true: a month with no events is a gap")
		}

		// The recorded zero is a month Gas cost nothing, so it counts — as the
		// minimum, and in the divisor behind the average.
		assertTrendSummary(t, got, projection.Trend{
			Observed: 2,
			Min:      0,
			Max:      4_000,
			Total:    4_000,
			Average:  2_000,
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
		// The log's range is still the log's range: the type is absent from it,
		// which is a month of gap, not an absence of months.
		assertTrendMonths(t, got, []projection.TrendMonth{
			{Month: "2026-01", Amount: 0, Recorded: false},
		})
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
