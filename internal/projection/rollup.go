package projection

import (
	"fmt"
	"maps"
	"slices"

	"github.com/Zaba505/expense-tracker/internal/domain"
	"github.com/Zaba505/expense-tracker/internal/money"
)

// Rollup is what a month came to: money out, money in, and what was left. It
// is the sheet's old `Monthly Bills Total` / `Income` / `Savings` block, with
// the difference that nothing here is a stored formula somebody has to keep
// dragging down a column — it is recomputed from the log on every read, so it
// cannot drift away from the events it summarizes.
//
// The same type totals a set of months (see Total), where Expenses is the
// `Total Expenditure` of the original sheet.
type Rollup struct {
	// Expenses is the sum of every expense-direction amount.
	Expenses money.Cents

	// Income is the sum of every income-direction amount.
	Income money.Cents
}

// Net is what was left over: income minus expenses. It is the sheet's
// `Savings` — the two are the same number, since a month's savings is
// whatever its income did not spend. Negative means the month spent more
// than it earned.
func (r Rollup) Net() money.Cents { return r.Income - r.Expenses }

// MonthRollup is one month's Rollup, labelled with the month.
type MonthRollup struct {
	Month string
	Rollup
}

// RollupByMonth summarizes each month of state by direction, oldest month
// first.
//
// It is a pure function of state and nothing else — no clock, no log, no
// second pass over the events — which is what lets a handler fold once and
// render the month view and its totals from the same value.
//
// Only months state actually mentions appear; RollupByMonth does not invent
// the empty months between them, because a month with no events is a month
// the log says nothing about, and a zero row would claim it earned and spent
// nothing. A type that was retired years ago and a type first used this
// morning are both just cells here: the totals are over whatever the log has
// for the month, so no column has to exist, or keep existing, for a month to
// add up.
func RollupByMonth(state State) ([]MonthRollup, error) {
	// Sized by months, not by cells: a decade of the log is a hundred-odd
	// months but thousands of (month, type, direction) cells, and this map
	// only ever holds the former.
	months := make(map[string]Rollup)
	for key, amount := range state {
		rollup := months[key.Month]
		switch key.Direction {
		case domain.DirectionExpense, "":
			// The zero direction is an expense — domain.Event.Normalize's
			// rule, and the one Fold keys by, so no folded state carries one.
			// A State assembled by hand can, and counting it as the expense
			// the domain says it is beats refusing to total the month over a
			// field the caller was entitled to leave at its default.
			rollup.Expenses += amount
		case domain.DirectionIncome:
			rollup.Income += amount
		default:
			// Unreachable from Fold, which refuses this event outright
			// (ErrUnknownDirection) rather than letting it reach a total.
			// This is the same guard for a State that did not come from one.
			return nil, fmt.Errorf("%w: %q (month %q, type %q)", ErrUnknownDirection, key.Direction, key.Month, key.Type)
		}
		months[key.Month] = rollup
	}

	// A month is "YYYY-MM" (domain's layout), which sorts lexicographically
	// into chronological order — so this is the year boundary handled too,
	// without parsing a date.
	rollups := make([]MonthRollup, 0, len(months))
	for _, month := range slices.Sorted(maps.Keys(months)) {
		rollups = append(rollups, MonthRollup{Month: month, Rollup: months[month]})
	}
	return rollups, nil
}

// Total adds rollups together: total expenditure (Expenses), total income
// (Income), and total saved (Net).
//
// It takes the months rather than the State so that the caller chooses the
// span — every month for the all-time totals, one year's worth for the yearly
// grid's bottom row — off a single fold, and so that the direction cases are
// decided in exactly one place, RollupByMonth. Summing an empty span is a
// zero Rollup, not an error: nothing recorded is nothing spent.
func Total(rollups []MonthRollup) Rollup {
	var total Rollup
	for _, rollup := range rollups {
		total.Expenses += rollup.Expenses
		total.Income += rollup.Income
	}
	return total
}
