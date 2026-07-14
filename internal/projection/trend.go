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

// Trend is one type's whole history: what it came to in every month of the
// log's range, and what those months came to between them.
//
// It answers a question the yearly grid cannot, because the grid stops at a
// year: whether a type is drifting up, and how this year's figure compares to
// the same type three years ago.
type Trend struct {
	Type string

	// Months is every month from the log's first to its last, oldest first,
	// including the ones this type says nothing about — see TrendMonth.Recorded.
	Months []TrendMonth

	// Observed is how many of Months the log actually recorded this type in.
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
// or one that has been renamed away and no longer exists under this name. The
// summaries of an empty trend are all zero, and a page has to say that the log
// is silent rather than claim the type cost nothing.
func (t Trend) Empty() bool { return t.Observed == 0 }

// TrendMonth is one month of a trend: what the type came to, and whether the
// log said anything about it at all.
type TrendMonth struct {
	Month  string
	Amount money.Cents

	// Recorded is whether the log has this type in this month.
	//
	// It is the whole difference between a gap and a zero, and the two are not
	// the same fact. A zero is a cell some event explicitly set to nothing —
	// how an entry is retired in a log that cannot delete one — and it says the
	// type cost nothing that month. A gap says the log is silent: the type did
	// not exist yet, or was not used, or the month was never entered at all.
	// Rendering the gap as $0.00 would put words in the log's mouth, and it
	// would be the same words it uses for a real zero.
	Recorded bool
}

// Gap reports whether the log is silent about this type in this month. It is
// the negation Recorded reads better as from a template, where the interesting
// case is the one that renders an em dash instead of an amount.
func (m TrendMonth) Gap() bool { return !m.Recorded }

// ProjectTrend turns state into one type's month-by-month history.
//
// The months are the log's full range — first month recorded to last, every
// month between them, whether or not this type appears in one — rather than
// only the months the type was used. That is what makes the shape of a type
// legible: the gaps before its first use are when it did not exist, the gaps
// after its last are its retirement, and a type nobody has entered since March
// shows that as four blank rows rather than as a report that simply ends.
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

	// A month lands in amounts only if some cell of this type mentions it, so
	// the map's keys are the observed months and a missing key is a gap. This
	// is why presence is read off the map rather than off a non-zero amount:
	// `amounts[month] += 0` still creates the key, which is exactly how a cell
	// explicitly set to zero stays a recorded month instead of becoming a gap.
	amounts := make(map[string]money.Cents)

	// The range is the log's, not this type's, so it is taken from every cell
	// of state rather than only the ones this trend is about.
	var first, last string

	for key, amount := range state {
		if first == "" || key.Month < first {
			first = key.Month
		}
		if key.Month > last {
			last = key.Month
		}

		if key.Type != typ {
			continue
		}

		switch key.Direction {
		case domain.DirectionExpense, domain.DirectionIncome, "":
			// The zero direction is an expense — domain.Event.Normalize's rule,
			// and the one Fold keys by — and a trend sums the directions
			// together anyway, so it lands in the same month either way.
		default:
			// Unreachable from Fold, which refuses the event outright. This is
			// the guard for a State that did not come from one, and it refuses
			// rather than sums: the yearly grid fails on this cell
			// (ErrUnknownDirection), and a trend that quietly counted what the
			// grid will not would have the two reports disagreeing about the
			// same month.
			return Trend{}, fmt.Errorf("%w: %q (month %q, type %q)", ErrUnknownDirection, key.Direction, key.Month, key.Type)
		}

		amounts[key.Month] += amount
	}

	// An empty log has no range, so there are no months to report and nothing
	// to summarize. That is not an error: a log with nothing in it says nothing
	// about this type, which is what an empty trend means.
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
		amount, recorded := amounts[month]

		trend.Months = append(trend.Months, TrendMonth{
			Month:    month,
			Amount:   amount,
			Recorded: recorded,
		})
		if !recorded {
			continue
		}

		// Seeded from the first observed month rather than from zero, so that a
		// type that only ever cost money does not report a minimum of $0.00 for
		// a month it was never used in.
		if trend.Observed == 0 || amount < trend.Min {
			trend.Min = amount
		}
		if trend.Observed == 0 || amount > trend.Max {
			trend.Max = amount
		}
		trend.Total += amount
		trend.Observed++
	}

	trend.Average = average(trend.Total, trend.Observed)

	return trend, nil
}
