package view

import (
	"net/url"
	"time"

	"github.com/Zaba505/expense-tracker/internal/domain"
	"github.com/Zaba505/expense-tracker/internal/money"
	"github.com/Zaba505/expense-tracker/internal/projection"
)

// EntriesPath is where the entry form posts an event. It lives here, beside
// the template that references it, for the reason AssetPrefix does: the URL in
// the markup and the route the handler is mounted on are one decision, and
// web mounts its handler on this same constant.
const EntriesPath = "/entries"

// PanelID names the month panel element, and PanelTarget is the selector htmx
// swaps it by.
//
// The panel is the swap unit — form, folded cells, and rollup in one element —
// because an appended event changes all three: the amount lands in a cell, the
// cell moves the month's totals, and a type used for the first time becomes an
// autocomplete option. Swapping only the form would leave the page showing
// totals that no longer match the log it just added to.
const (
	PanelID     = "month-panel"
	PanelTarget = "#" + PanelID
)

// typesListID ties the type input to the datalist of types the log has seen.
const typesListID = "known-types"

// Panel is one month as the page shows it: the form that adds to it, what the
// log currently folds to for that month, and what the month came to.
//
// Everything in it is derived from the log on the way past — nothing here is
// stored — so a Panel is only ever as current as the fold that built it.
type Panel struct {
	// Month is the month being shown, "YYYY-MM". It is the month the form
	// last submitted for, or the current one on a fresh page.
	Month string

	// Rows are the folded cells for Month, in a stable order.
	Rows []Row

	// Rollup is what Month came to: out, in, and what was left.
	Rollup projection.Rollup

	// KnownTypes are the types the whole log has mentioned, most recently used
	// first. They are the form's autocomplete, and they span the log rather
	// than Month: last January's "Insurance" is worth suggesting in July.
	KnownTypes []projection.KnownType

	// Events is the month's audit trail, in the log's order.
	Events []domain.Event

	// Form is the entry form's state — what is typed in it, and what was
	// wrong with it.
	Form Form
}

// Row is one folded cell: what a type came to in this month, in one
// direction.
//
// A row can be zero, and one that is stays on the page. A zero cell is not an
// absent one: it is a cell some event explicitly set to nothing — how a
// wrong-direction entry is retired in an append-only log, since it cannot be
// deleted (see projection.Key). Hiding it would hide the correction and leave
// the owner staring at a month whose rows no longer explain its total.
type Row struct {
	Type      string
	Direction domain.Direction
	Amount    money.Cents
}

// Field is one input of the entry form: what the user typed, and why it was
// refused.
//
// Value is echoed back on a refusal so that a rejected submission is corrected
// rather than retyped — the amount that failed to parse is still in the box,
// with the reason under it.
type Field struct {
	Value string
	Error string
}

// Form is the state of the entry form: its five inputs, each carrying the
// value it shows and the message under it, if any.
type Form struct {
	Month      Field
	Type       Field
	Amount     Field
	Direction  Field
	Action     Field
	Note       Field
	RefEventID Field
}

// NewForm is the empty form for a month: expense, add, nothing typed yet.
//
// The defaults are the domain's own — a direction the log defaults to
// (domain.Event.Normalize's rule) and the action an entry records
// (domain.ActionAdd) — so the form a user submits without touching either
// records exactly what an untouched form claims it does.
func NewForm(month string) Form {
	return Form{
		Month:     Field{Value: month},
		Direction: Field{Value: string(domain.DirectionExpense)},
		Action:    Field{Value: string(domain.ActionAdd)},
	}
}

// Cleared is the form as it comes back after an event was recorded: the type
// and the amount are emptied, ready for the next one, and the month, the
// direction, and the action stay as they were.
//
// Entries come in runs — a month's worth of bills is typed in one sitting —
// so the fields that are the same every time are the ones worth keeping, and
// the two that change are the two that get the focus.
func (f Form) Cleared() Form {
	return Form{
		Month:     Field{Value: f.Month.Value},
		Direction: Field{Value: f.Direction.Value},
		Action:    Field{Value: f.Action.Value},
	}
}

// Rejected reports whether any field carries a message, which is to say
// whether the submission was refused and nothing was appended.
func (f Form) Rejected() bool {
	for _, field := range []Field{f.Month, f.Type, f.Amount, f.Direction, f.Action, f.Note, f.RefEventID} {
		if field.Error != "" {
			return true
		}
	}
	return false
}

// DirectionIs and ActionIs report whether the form currently holds the given
// choice, which is what decides the checked radio and the selected option.
// They compare against the domain's constants rather than against string
// literals in the markup, so the values the form posts are the values the log
// stores.
func (f Form) DirectionIs(d domain.Direction) bool { return f.Direction.Value == string(d) }

func (f Form) ActionIs(a domain.Action) bool { return f.Action.Value == string(a) }

// CorrectionPath is the month-view link that pre-fills the form to append a
// compensating event against e.
func (p Panel) CorrectionPath(e domain.Event) string {
	query := url.Values{
		"type":       {e.Type},
		"direction":  {string(e.Direction)},
		"action":     {string(e.Action)},
		"refEventId": {e.ID},
	}
	return "/month/" + p.Month + "?" + query.Encode()
}

// CorrectionLabel is the action the month view offers for e: adds are adjusted,
// sets are overridden.
func CorrectionLabel(e domain.Event) string {
	if e.Action == domain.ActionSet {
		return "Override"
	}
	return "Adjust"
}

// CanVoid reports whether e can be walked back with one compensating entry from
// the month view.
func CanVoid(e domain.Event) bool { return e.Action == domain.ActionAdd }

// VoidAmount is the amount a compensating add records to retire e.
func VoidAmount(e domain.Event) string { return (-e.Amount).String() }

// VoidNote is the note the month view stamps onto a one-click void.
func VoidNote(e domain.Event) string { return "voids " + e.ID }

// RecordedAtDisplay is the audit-trail rendering of one event timestamp.
func RecordedAtDisplay(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05 UTC") }
