package projection

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Zaba505/expense-tracker/internal/domain"
	"github.com/Zaba505/expense-tracker/internal/money"
)

// ErrInvalidType reports that a trend was asked for without naming a type. It
// is a programming error rather than an empty report: every type in the log is
// a trimmed, non-empty string (domain.Event.Normalize's rule), so no type is
// spelled "", and a trend of nothing is a question nobody meant to ask.
var ErrInvalidType = errors.New("projection: invalid type")

// Trend is one type's history: what it came to in every month from its first
// recorded month to its last, and what those months came to between them.
//
// It answers a question the yearly grid cannot, because the grid stops at a
// year: whether a type is drifting up, and how this year's figure compares to
// the same type three years ago.
type Trend struct {
	Type string

	// Months runs from the type's first recorded month to its last, oldest
	// first, including the months in between that it came to nothing in — see
	// TrendMonth.Recorded.
	Months []TrendMonth

	// Observed is how many of Months the type actually came to something in.
	// It is the divisor behind Average and the count the summaries are over,
	// so a page can say what "average" was an average of.
	Observed int

	// Min, Max, Total and Average are over the observed months alone — the gaps
	// are not zeroes to be averaged in. See ProjectTrend.
	Min     money.Cents
	Max     money.Cents
	Total   money.Cents
	Average money.Cents
}

// Empty reports whether the log has nothing for this type — a type never used,
// one renamed away, or one whose every entry has since been voided. The
// summaries of an empty trend are all zero, and a page has to say that the log
// is silent rather than claim the type cost nothing.
func (t Trend) Empty() bool { return t.Observed == 0 }

// TrendMonth is one month of a trend: what the type came to, and whether it
// came to anything at all.
type TrendMonth struct {
	Month  string
	Amount money.Cents

	// Recorded is whether the type came to anything in this month.
	//
	// A month it came to nothing in is a gap, and that covers two cases the log
	// cannot tell apart and this report must not pretend to: a month no event
	// ever mentioned the type in, and a month whose events cancel out. The
	// second is what a void is — the month view retires an entry by appending
	// the opposite of it (view.VoidAmount), because an append-only log cannot
	// delete one — so the cell survives at zero, and the only honest reading of
	// it is the one the owner meant: that entry never happened.
	//
	// Counting it as an observed $0.00 instead would make the void that fixes a
	// mistake into a month of history: a type voided out of one month would
	// report a minimum of $0.00 forever, and its average would be divided by a
	// month it was never spent in.
	Recorded bool
}

// Gap reports whether the type came to nothing in this month. It is the
// negation Recorded reads better as from a template, where the interesting case
// is the one that renders an em dash instead of an amount.
func (m TrendMonth) Gap() bool { return !m.Recorded }

// ProjectTrend turns state into one type's month-by-month history.
//
// The months run from the type's first recorded month to its last, and every
// month between them is a row whether the type was used in it or not — so the
// gaps in the middle, the months it lapsed, are visible as gaps rather than
// closed up into a continuous series that never happened.
//
// The range is the type's own and not the whole log's, which costs the report
// the leading and trailing gaps that would frame a type's introduction and
// retirement. It buys something worth more than they are. State is the fold of
// an append-only log, so no key ever leaves it: a range taken from every key
// would be anchored by the log's most extreme month whatever type it belonged
// to, and one entry mistyped into the year 216 — a slip in the month input's
// year segment, which domain.ValidMonth accepts, since it is a perfectly
// well-formed month — would put twenty thousand mostly-empty rows on every
// type's trend, permanently. Nothing the owner could append would take it back.
// Scoped to the type, a mistyped month can only distort the trend of the type
// it was entered against, where its own first row names it; and voiding it —
// the correction the owner would reach for anyway — takes it out of the range
// entirely, because a voided month comes to nothing and a month that comes to
// nothing is not a recorded one. The report heals when the log is corrected,
// which is the property that matters in a log that cannot be edited.
//
// Each month's amount sums both directions, as the yearly grid's cells do. In
// practice a type is only ever one direction, so this sums nothing the owner
// thinks of as separate; when a type really was recorded both ways, the trend
// is of the type, and the expense/income split is the month view's business.
//
// The summaries — Min, Max, Total, Average — are over the observed months
// alone. Averaging the gaps in as zeroes would divide a type's spending by the
// months it did not exist for, so a type used twice in a decade would report an
// average near zero and a real habit would look like it was tailing off every
// month nothing was entered. The count those summaries are over is Observed, so
// the page can say so.
//
// Renames need no handling here, and that is not an oversight: Fold's state is
// already canonical (see canonicalize), so a rename of "Fuel" into "Gas" has
// rewritten the history behind it, and the trend of "Gas" reaches back through
// the months that were recorded as "Fuel" without this projection knowing that
// a rename ever happened. The trend of "Fuel" is correspondingly empty — which
// is the truth about a name the log no longer reads anything under.
func ProjectTrend(state State, typ string) (Trend, error) {
	typ = strings.TrimSpace(typ)
	if typ == "" {
		return Trend{}, fmt.Errorf("%w: type is empty", ErrInvalidType)
	}

	trend := Trend{Type: typ}

	amounts := make(map[string]money.Cents)
	for key, amount := range state {
		// Checked over every cell, not only this type's, so that a State the
		// yearly grid would refuse (ErrUnknownDirection) is one this refuses
		// too. Filtering first would make the trend the one report that reads a
		// log the others cannot, and two reports disagreeing about whether the
		// log can be read at all is worse than either answer.
		switch key.Direction {
		case domain.DirectionExpense, domain.DirectionIncome, "":
			// The zero direction is an expense — domain.Event.Normalize's rule,
			// and the one Fold keys by — and a trend sums the directions
			// together anyway, so it lands in the same month either way.
		default:
			// Unreachable from Fold, which refuses the event outright. This is
			// the guard for a State that did not come from one.
			return Trend{}, fmt.Errorf("%w: %q (month %q, type %q)", ErrUnknownDirection, key.Direction, key.Month, key.Type)
		}

		if key.Type != typ {
			continue
		}
		amounts[key.Month] += amount
	}

	// The range is taken from the months the type came to something in, so a
	// cell that nets to zero — a void, or an entry walked back to nothing —
	// neither starts the report nor extends it.
	var first, last string
	for month, amount := range amounts {
		if amount == 0 {
			continue
		}
		if first == "" || month < first {
			first = month
		}
		if month > last {
			last = month
		}
	}

	// A type the log has nothing for has no range, so there are no months to
	// report and nothing to summarize. That is not an error: it is what the log
	// says about a type never used, renamed away, or voided back out again.
	if first == "" {
		return trend, nil
	}

	// The range is walked as time rather than as strings so that the year
	// boundary is December's successor and not a month called "2026-13".
	start, err := domain.ParseMonth(first)
	if err != nil {
		return Trend{}, fmt.Errorf("projection: trend range starts at %w", err)
	}
	end, err := domain.ParseMonth(last)
	if err != nil {
		return Trend{}, fmt.Errorf("projection: trend range ends at %w", err)
	}

	for t := start; !t.After(end); t = t.AddDate(0, 1, 0) {
		month := domain.Month(t)
		amount := amounts[month]
		recorded := amount != 0

		trend.Months = append(trend.Months, TrendMonth{
			Month:    month,
			Amount:   amount,
			Recorded: recorded,
		})
		if !recorded {
			continue
		}

		// Seeded from the first observed month rather than from the zero value,
		// so that a type that only ever cost money does not report a minimum of
		// $0.00 for a month it was never spent in.
		if trend.Observed == 0 {
			trend.Min, trend.Max = amount, amount
		} else {
			trend.Min = min(trend.Min, amount)
			trend.Max = max(trend.Max, amount)
		}
		trend.Total += amount
		trend.Observed++
	}

	trend.Average = average(trend.Total, trend.Observed)

	return trend, nil
}
