package view

import (
	"net/url"

	"github.com/Zaba505/expense-tracker/internal/projection"
)

// TrendsPath is where the per-type trend report lives, and where its type
// picker submits. It lives here, beside the template that references it, for
// the reason EntriesPath does: the URL in the markup and the route the handler
// is mounted on are one decision, and web mounts its handler on this same
// constant.
const TrendsPath = "/reports/types"

// TrendPath is the address of one type's trend.
//
// The type travels as a query parameter rather than as a path segment — unlike
// the month and the year, which are both path wildcards — because a type is
// free-form text the owner typed, and the things they type are not all
// path-safe. "Gas & Electric" and "Dining / Takeout" are perfectly good type
// names; the slash in the second one is a route boundary, not a character, so a
// path segment would silently address a different report than the one the link
// says. Escaped into a query parameter, every type the log accepts is
// addressable.
func TrendPath(typ string) string {
	return TrendsPath + "?" + url.Values{TrendTypeParam: []string{typ}}.Encode()
}

// TrendTypeParam is the query parameter the picker submits the type under.
//
// The form input, the link builder, and the handler that reads them all name it
// through this one constant, for the reason TrendsPath exists: the key a link
// is written with and the key the handler looks up are one decision, and a
// second spelling of it is one that can drift into a report that always renders
// its own picker back.
const TrendTypeParam = "type"

// TrendPage is the per-type trend report as the page shows it: the type's
// history, and the picker that chose it.
//
// Like every other read model here, nothing in it is stored — it is what the
// log folded to on the way past.
type TrendPage struct {
	// Trend is the picked type's history across the log's full range. It is the
	// zero Trend until a type is picked — see Selected.
	Trend projection.Trend

	// KnownTypes are the types the log has mentioned, most recently used first.
	// They are the picker's autocomplete, and they are the same list the entry
	// form offers, so a type that can be entered can be charted.
	KnownTypes []projection.KnownType
}

// Selected reports whether a type has been picked yet.
//
// It is read off the trend rather than carried as its own flag so the two
// cannot disagree: projection.ProjectTrend refuses an empty type, so a Trend
// with no name is one that was never projected, and the page shows the picker
// alone. That is a different page from a picked type the log happens to have
// nothing for (projection.Trend.Empty), which says so — the first has no answer
// yet, the second has one and it is "nothing".
func (p TrendPage) Selected() bool { return p.Trend.Type != "" }
