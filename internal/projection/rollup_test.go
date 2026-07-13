package projection_test

import (
	"errors"
	"maps"
	"testing"
	"testing/quick"

	"github.com/Zaba505/expense-tracker/internal/domain"
	"github.com/Zaba505/expense-tracker/internal/money"
	"github.com/Zaba505/expense-tracker/internal/projection"
)

func TestRollupByMonth(t *testing.T) {
	t.Run("table cases", func(t *testing.T) {
		tests := map[string]struct {
			state projection.State
			want  []projection.MonthRollup
		}{
			"empty state rolls up to nothing": {
				state: projection.State{},
				want:  []projection.MonthRollup{},
			},
			"expenses only": {
				state: projection.State{
					expense("2026-07", "Rent"):      120_000,
					expense("2026-07", "Groceries"): 45_000,
				},
				want: []projection.MonthRollup{
					{Month: "2026-07", Rollup: projection.Rollup{Expenses: 165_000, Income: 0}},
				},
			},
			"income only": {
				state: projection.State{
					income("2026-07", "Paycheck"): 500_000,
					income("2026-07", "Interest"): 1_234,
				},
				want: []projection.MonthRollup{
					{Month: "2026-07", Rollup: projection.Rollup{Expenses: 0, Income: 501_234}},
				},
			},
			"a month that saved": {
				state: projection.State{
					expense("2026-07", "Rent"):     120_000,
					income("2026-07", "Paycheck"):  500_000,
					expense("2026-07", "Gas"):      8_000,
					income("2026-07", "Dividends"): 2_000,
				},
				want: []projection.MonthRollup{
					{Month: "2026-07", Rollup: projection.Rollup{Expenses: 128_000, Income: 502_000}},
				},
			},
			"a month that overspent": {
				state: projection.State{
					expense("2026-07", "Mortgage"): 250_000,
					income("2026-07", "Paycheck"):  100_000,
				},
				want: []projection.MonthRollup{
					{Month: "2026-07", Rollup: projection.Rollup{Expenses: 250_000, Income: 100_000}},
				},
			},
			"a corrected overstatement nets out of the expense total": {
				// A negative add is how the log walks a mistake back, so a
				// cell can be negative and the total has to take it as it is.
				state: projection.State{
					expense("2026-07", "Groceries"): 45_000,
					expense("2026-07", "Toll Pass"): -1_000,
				},
				want: []projection.MonthRollup{
					{Month: "2026-07", Rollup: projection.Rollup{Expenses: 44_000}},
				},
			},
			"one type recorded both ways stays two amounts": {
				// A grocery rebate is income; it does not shrink the grocery
				// bill, it shows up on the other side of the ledger.
				state: projection.State{
					expense("2026-07", "Groceries"): 45_000,
					income("2026-07", "Groceries"):  2_500,
				},
				want: []projection.MonthRollup{
					{Month: "2026-07", Rollup: projection.Rollup{Expenses: 45_000, Income: 2_500}},
				},
			},
			"months come back oldest first, across the year boundary": {
				state: projection.State{
					expense("2026-01", "Rent"): 3,
					expense("2025-12", "Rent"): 2,
					expense("2026-10", "Rent"): 4,
					expense("2025-02", "Rent"): 1,
				},
				want: []projection.MonthRollup{
					{Month: "2025-02", Rollup: projection.Rollup{Expenses: 1}},
					{Month: "2025-12", Rollup: projection.Rollup{Expenses: 2}},
					{Month: "2026-01", Rollup: projection.Rollup{Expenses: 3}},
					{Month: "2026-10", Rollup: projection.Rollup{Expenses: 4}},
				},
			},
			"a month mixing a retired type and a newly-appeared one": {
				// April 2026 is the month the owner bought a home. Rent is
				// being retired (a final, prorated week of it) and Renters
				// Insurance is simply gone; Mortgage and Home Insurance appear
				// for the first time. The old sheet could not express this
				// without deleting a column's history — here every type is
				// just a cell that a month either has or does not, and the
				// rollup totals whatever the log says was there.
				state: projection.State{
					// March: the last full month of the old life.
					expense("2026-03", "Rent"):              120_000,
					expense("2026-03", "Renters Insurance"): 1_500,
					expense("2026-03", "Groceries"):         45_000,
					income("2026-03", "Paycheck"):           500_000,

					// April: retiring and newly-appeared types, side by side.
					expense("2026-04", "Rent"):           30_000, // prorated, and the last one ever
					expense("2026-04", "Mortgage"):       210_000,
					expense("2026-04", "Home Insurance"): 9_000,
					expense("2026-04", "Groceries"):      45_000,
					income("2026-04", "Paycheck"):        500_000,

					// May: the old types are gone and nothing misses them.
					expense("2026-05", "Mortgage"):       210_000,
					expense("2026-05", "Home Insurance"): 9_000,
					expense("2026-05", "Groceries"):      45_000,
					income("2026-05", "Paycheck"):        500_000,
				},
				want: []projection.MonthRollup{
					{Month: "2026-03", Rollup: projection.Rollup{Expenses: 166_500, Income: 500_000}},
					{Month: "2026-04", Rollup: projection.Rollup{Expenses: 294_000, Income: 500_000}},
					{Month: "2026-05", Rollup: projection.Rollup{Expenses: 264_000, Income: 500_000}},
				},
			},
		}

		for name, tt := range tests {
			t.Run(name, func(t *testing.T) {
				got, err := projection.RollupByMonth(tt.state)
				if err != nil {
					t.Fatalf("RollupByMonth() error = %v", err)
				}
				assertRollups(t, got, tt.want)
			})
		}
	})

	t.Run("net is income minus expenses", func(t *testing.T) {
		tests := map[string]struct {
			rollup projection.Rollup
			want   money.Cents
		}{
			"saved":       {projection.Rollup{Expenses: 128_000, Income: 502_000}, 374_000},
			"overspent":   {projection.Rollup{Expenses: 250_000, Income: 100_000}, -150_000},
			"broke even":  {projection.Rollup{Expenses: 100_000, Income: 100_000}, 0},
			"zero rollup": {projection.Rollup{}, 0},
		}

		for name, tt := range tests {
			t.Run(name, func(t *testing.T) {
				if got := tt.rollup.Net(); got != tt.want {
					t.Errorf("Rollup%+v.Net() = %s, want %s", tt.rollup, got, tt.want)
				}
			})
		}
	})

	t.Run("a hand-built key with no direction counts as an expense", func(t *testing.T) {
		// Key is exported and Direction is a plain field, so a caller can
		// leave it at its zero value — which the domain defines as an expense.
		// Fold never emits one (it normalizes first), but refusing to total a
		// month over a default the caller was entitled to take would be the
		// projection disagreeing with the domain about what an empty direction
		// means.
		got, err := projection.RollupByMonth(projection.State{
			{Month: "2026-07", Type: "Rent"}: 120_000,
			income("2026-07", "Paycheck"):    500_000,
		})
		if err != nil {
			t.Fatalf("RollupByMonth() error = %v", err)
		}
		assertRollups(t, got, []projection.MonthRollup{
			{Month: "2026-07", Rollup: projection.Rollup{Expenses: 120_000, Income: 500_000}},
		})
	})

	t.Run("unknown directions fail loudly", func(t *testing.T) {
		got, err := projection.RollupByMonth(projection.State{
			expense("2026-07", "Rent"):                                  120_000,
			{Month: "2026-07", Type: "Transfer", Direction: "transfer"}: 5_000,
		})
		if got != nil {
			t.Errorf("RollupByMonth() returned partial rollups %#v, want nil on an unknown direction", got)
		}
		if !errors.Is(err, projection.ErrUnknownDirection) {
			t.Fatalf("RollupByMonth() error = %v, want ErrUnknownDirection", err)
		}
	})

	t.Run("the state is left alone", func(t *testing.T) {
		state := projection.State{
			expense("2026-07", "Rent"):    120_000,
			income("2026-07", "Paycheck"): 500_000,
		}
		want := maps.Clone(state)

		if _, err := projection.RollupByMonth(state); err != nil {
			t.Fatalf("RollupByMonth() error = %v", err)
		}
		if !maps.Equal(state, want) {
			t.Errorf("RollupByMonth() mutated its input: state = %#v, want %#v", state, want)
		}
	})

	t.Run("property: every month's amounts land in exactly one direction", func(t *testing.T) {
		// Whatever the log holds, each cell is counted once and on the side it
		// was recorded on: the two totals partition the state, and net is the
		// gap between them.
		if err := quick.Check(func(amounts []int64) bool {
			state := make(projection.State)
			var wantExpenses, wantIncome money.Cents
			for i, amount := range amounts {
				typ := string(rune('A' + i%26))
				if i%2 == 0 {
					state[expense("2026-07", typ)] += money.Cents(amount)
					wantExpenses += money.Cents(amount)
					continue
				}
				state[income("2026-07", typ)] += money.Cents(amount)
				wantIncome += money.Cents(amount)
			}

			got, err := projection.RollupByMonth(state)
			if err != nil {
				return false
			}
			if len(amounts) == 0 {
				return len(got) == 0
			}
			return len(got) == 1 &&
				got[0].Month == "2026-07" &&
				got[0].Expenses == wantExpenses &&
				got[0].Income == wantIncome &&
				got[0].Net() == wantIncome-wantExpenses
		}, nil); err != nil {
			t.Error(err)
		}
	})
}

func TestTotal(t *testing.T) {
	t.Run("totals a span of months", func(t *testing.T) {
		// Total expenditure is the whole log's expenses: every month's, added
		// up, retired types and all.
		state := projection.State{
			expense("2026-03", "Rent"):     120_000,
			income("2026-03", "Paycheck"):  500_000,
			expense("2026-04", "Mortgage"): 210_000,
			income("2026-04", "Paycheck"):  500_000,
			expense("2026-05", "Mortgage"): 210_000,
			income("2026-05", "Paycheck"):  400_000,
		}

		rollups, err := projection.RollupByMonth(state)
		if err != nil {
			t.Fatalf("RollupByMonth() error = %v", err)
		}

		got := projection.Total(rollups)
		want := projection.Rollup{Expenses: 540_000, Income: 1_400_000}
		if got != want {
			t.Errorf("Total() = %+v, want %+v", got, want)
		}
		if got.Net() != 860_000 {
			t.Errorf("Total().Net() = %s, want 8600.00", got.Net())
		}
	})

	t.Run("totals a single year out of a longer log", func(t *testing.T) {
		// The yearly grid totals one year off the same fold, which is the
		// whole reason Total takes the months rather than the State.
		state := projection.State{
			expense("2025-12", "Rent"):     100_000,
			expense("2026-01", "Rent"):     120_000,
			income("2026-01", "Paycheck"):  500_000,
			expense("2026-02", "Rent"):     120_000,
			expense("2027-01", "Mortgage"): 999_999,
		}

		rollups, err := projection.RollupByMonth(state)
		if err != nil {
			t.Fatalf("RollupByMonth() error = %v", err)
		}

		var of2026 []projection.MonthRollup
		for _, rollup := range rollups {
			if rollup.Month[:4] == "2026" {
				of2026 = append(of2026, rollup)
			}
		}

		got := projection.Total(of2026)
		want := projection.Rollup{Expenses: 240_000, Income: 500_000}
		if got != want {
			t.Errorf("Total(2026) = %+v, want %+v", got, want)
		}
	})

	t.Run("nothing recorded is nothing spent", func(t *testing.T) {
		for name, rollups := range map[string][]projection.MonthRollup{
			"nil":   nil,
			"empty": {},
		} {
			t.Run(name, func(t *testing.T) {
				if got := projection.Total(rollups); got != (projection.Rollup{}) {
					t.Errorf("Total(%v) = %+v, want the zero Rollup", rollups, got)
				}
			})
		}
	})
}

// TestFoldRollup is the whole read path the month view will use: append-only
// events in, the numbers the user reads out.
func TestFoldRollup(t *testing.T) {
	events := []domain.Event{
		// March, entered as the month went along.
		event(domain.ActionSet, "2026-03", "Rent", 120_000),
		incomeEvent(domain.ActionSet, "2026-03", "Paycheck", 500_000),
		event(domain.ActionAdd, "2026-03", "Groceries", 30_000),
		event(domain.ActionAdd, "2026-03", "Groceries", 15_000),
		// A grocery run that was double-entered, walked back.
		event(domain.ActionAdd, "2026-03", "Groceries", -15_000),

		// April: Rent retires mid-month, Mortgage appears.
		event(domain.ActionSet, "2026-04", "Rent", 30_000),
		event(domain.ActionSet, "2026-04", "Mortgage", 210_000),
		incomeEvent(domain.ActionSet, "2026-04", "Paycheck", 500_000),
		// The security deposit came back — income, not a smaller rent.
		incomeEvent(domain.ActionAdd, "2026-04", "Rent", 100_000),
	}

	state, err := projection.Fold(events)
	if err != nil {
		t.Fatalf("Fold() error = %v", err)
	}

	got, err := projection.RollupByMonth(state)
	if err != nil {
		t.Fatalf("RollupByMonth() error = %v", err)
	}

	want := []projection.MonthRollup{
		{Month: "2026-03", Rollup: projection.Rollup{Expenses: 150_000, Income: 500_000}},
		{Month: "2026-04", Rollup: projection.Rollup{Expenses: 240_000, Income: 600_000}},
	}
	assertRollups(t, got, want)

	if net := got[0].Net(); net != 350_000 {
		t.Errorf("2026-03 net = %s, want 3500.00", net)
	}
	if net := got[1].Net(); net != 360_000 {
		t.Errorf("2026-04 net = %s, want 3600.00", net)
	}

	total := projection.Total(got)
	if want := (projection.Rollup{Expenses: 390_000, Income: 1_100_000}); total != want {
		t.Errorf("Total() = %+v, want %+v", total, want)
	}
}

// TestCorrectAMisdirectedEntry pins the workflow Key's doc promises, because
// it is the sharp edge of keying a cell by direction: an entry recorded in the
// wrong direction takes two events to correct, and getting it wrong by one
// event leaves the month counting the same money on both sides of the ledger.
func TestCorrectAMisdirectedEntry(t *testing.T) {
	// Expense is the default direction, so this is the mistake that actually
	// happens: a paycheck entered without saying it was income.
	misdirected := []domain.Event{
		event(domain.ActionSet, "2026-07", "Paycheck", 500_000),
	}

	t.Run("setting it in the right direction alone double-counts it", func(t *testing.T) {
		state, err := projection.Fold(append(misdirected,
			incomeEvent(domain.ActionSet, "2026-07", "Paycheck", 500_000),
		))
		if err != nil {
			t.Fatalf("Fold() error = %v", err)
		}

		got, err := projection.RollupByMonth(state)
		if err != nil {
			t.Fatalf("RollupByMonth() error = %v", err)
		}

		// The income cell is right and the expense cell is still standing.
		// Net is $5,000 short of the truth, and this is why the entry path
		// owes the user the second event rather than the fold guessing.
		assertRollups(t, got, []projection.MonthRollup{
			{Month: "2026-07", Rollup: projection.Rollup{Expenses: 500_000, Income: 500_000}},
		})
		if net := got[0].Net(); net != 0 {
			t.Errorf("net = %s, want 0.00 (the uncorrected month double-counts)", net)
		}
	})

	t.Run("retiring the wrong cell as well makes the month whole", func(t *testing.T) {
		state, err := projection.Fold(append(misdirected,
			incomeEvent(domain.ActionSet, "2026-07", "Paycheck", 500_000),
			event(domain.ActionSet, "2026-07", "Paycheck", 0),
		))
		if err != nil {
			t.Fatalf("Fold() error = %v", err)
		}

		got, err := projection.RollupByMonth(state)
		if err != nil {
			t.Fatalf("RollupByMonth() error = %v", err)
		}

		assertRollups(t, got, []projection.MonthRollup{
			{Month: "2026-07", Rollup: projection.Rollup{Expenses: 0, Income: 500_000}},
		})
		if net := got[0].Net(); net != 500_000 {
			t.Errorf("net = %s, want 5000.00", net)
		}
	})
}

func expense(month, typ string) projection.Key {
	return projection.Key{Month: month, Type: typ, Direction: domain.DirectionExpense}
}

func income(month, typ string) projection.Key {
	return projection.Key{Month: month, Type: typ, Direction: domain.DirectionIncome}
}

func assertRollups(t *testing.T, got, want []projection.MonthRollup) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("RollupByMonth() = %+v (%d months), want %+v (%d months)", got, len(got), want, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("RollupByMonth()[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}
