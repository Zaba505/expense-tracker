package projection

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/Zaba505/expense-tracker/internal/domain"
	"github.com/Zaba505/expense-tracker/internal/money"
)

// ErrInvalidYear reports that a yearly projection was asked for something that
// is not a calendar year in "YYYY" form.
var ErrInvalidYear = errors.New("projection: invalid year")

const (
	yearLayout   = "2006"
	monthsInYear = 12
)

// Year is one calendar year's grid: every month as a row, the year's distinct
// types as columns, and footer summaries computed from the same state.
type Year struct {
	Year    string
	Types   []string
	Months  []YearMonth
	Total   YearSummary
	Average YearSummary
}

// YearMonth is one month in the yearly grid.
type YearMonth struct {
	Month   string
	Amounts map[string]money.Cents
	Rollup  Rollup
}

// Amount is the month's total for one type, or zero when the month never
// mentioned it.
func (m YearMonth) Amount(typ string) money.Cents { return m.Amounts[typ] }

// YearSummary is the bottom-row summary for a year: per-type totals plus the
// rollups that go with them.
type YearSummary struct {
	Amounts map[string]money.Cents
	Rollup  Rollup
}

// Amount is the summary's total for one type, or zero when the year never
// mentioned it.
func (s YearSummary) Amount(typ string) money.Cents { return s.Amounts[typ] }

// ProjectYear turns state into one year's grid.
//
// The rows are every month of the requested year, including the empty ones, so
// the yearly report keeps the spreadsheet's shape while the columns stay driven
// by the log. Type columns are the union of types the state mentions anywhere in
// that year, and each cell is the month's amount for that type across both
// directions; the expense/income split stays visible in the rollup columns.
func ProjectYear(state State, year string) (Year, error) {
	if _, err := time.Parse(yearLayout, year); err != nil {
		return Year{}, fmt.Errorf("%w: %q", ErrInvalidYear, year)
	}

	report := Year{
		Year:   year,
		Months: make([]YearMonth, monthsInYear),
		Total: YearSummary{
			Amounts: make(map[string]money.Cents),
		},
		Average: YearSummary{
			Amounts: make(map[string]money.Cents),
		},
	}

	monthIndex := make(map[string]int, monthsInYear)
	for i := range monthsInYear {
		month := fmt.Sprintf("%s-%02d", year, i+1)
		report.Months[i] = YearMonth{
			Month:   month,
			Amounts: make(map[string]money.Cents),
		}
		monthIndex[month] = i
	}

	types := make(map[string]struct{})
	for key, amount := range state {
		i, ok := monthIndex[key.Month]
		if !ok {
			continue
		}

		report.Months[i].Amounts[key.Type] += amount
		report.Total.Amounts[key.Type] += amount
		types[key.Type] = struct{}{}

		switch key.Direction {
		case domain.DirectionExpense, "":
			// The zero value means "expense" in the older projection state too, so a
			// report that folds existing data must treat it the same way.
			report.Months[i].Rollup.Expenses += amount
			report.Total.Rollup.Expenses += amount
		case domain.DirectionIncome:
			report.Months[i].Rollup.Income += amount
			report.Total.Rollup.Income += amount
		default:
			return Year{}, fmt.Errorf("%w: %q (month %q, type %q)", ErrUnknownDirection, key.Direction, key.Month, key.Type)
		}
	}

	report.Types = slices.Sorted(maps.Keys(types))
	for _, typ := range report.Types {
		report.Average.Amounts[typ] = average(report.Total.Amounts[typ], monthsInYear)
	}
	report.Average.Rollup = Rollup{
		Expenses: average(report.Total.Rollup.Expenses, monthsInYear),
		Income:   average(report.Total.Rollup.Income, monthsInYear),
	}

	return report, nil
}

func average(total money.Cents, over int) money.Cents {
	if over <= 0 {
		return 0
	}

	divisor := int64(over)
	cents := int64(total)
	if cents < 0 {
		return money.Cents((cents - divisor/2) / divisor)
	}
	return money.Cents((cents + divisor/2) / divisor)
}
