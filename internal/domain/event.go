package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Zaba505/expense-tracker/internal/money"
)

// Action says how an event's amount combines with the ones before it for
// the same month and type.
//
// The set is open: a new action is a new constant here plus a case in the
// fold, and no migration — every event already in the log keeps meaning
// exactly what it meant when it was written. That is the whole reason the
// action is carried on the event instead of being implied by the
// collection it lives in.
type Action string

const (
	// ActionAdd sums with the running amount: two adds of $10 make $20.
	// It is what an entry, or a correction of one, records.
	ActionAdd Action = "add"

	// ActionSet supersedes the running amount: the last set wins. It is
	// what the spreadsheet import records, since a cell is a total rather
	// than a transaction.
	ActionSet Action = "set"
)

// Direction says which way money moved. It is a flag on the event rather
// than a separate income log, so a rollup like net or savings rate is a
// projection over one log instead of a join across two.
type Direction string

const (
	// DirectionExpense is money out. It is the default: the zero
	// Direction normalizes to it, so every entry path that does not
	// mention direction records an expense.
	DirectionExpense Direction = "expense"

	// DirectionIncome is money in.
	DirectionIncome Direction = "income"
)

// monthLayout is the reference layout for Event.Month: a calendar month,
// zero-padded, no day. It sorts lexicographically in chronological order,
// which is what lets a store order and range over months as plain strings.
const monthLayout = "2006-01"

// timeResolution is the resolution the log keeps timestamps at. It is
// microseconds because that is the finest precision every store
// round-trips (Firestore's), and an event that came back coarser than it
// went in would sort against its neighbors differently after a reload
// than it did before one.
const timeResolution = time.Microsecond

// ErrInvalidEvent is the sentinel behind every Validate failure, wrapped
// with the field at fault. Callers that need to tell a bad submission
// from a broken database — the web layer answering 400 versus 500, the
// importer naming the offending row — match it with errors.Is.
var ErrInvalidEvent = errors.New("domain: invalid event")

// Event is one immutable fact: an amount recorded against a month and a
// type at a moment in time. Events are only ever appended. Nothing edits
// or deletes one — a mistake is corrected by appending another event that
// says so, which is why history stays reconstructible and why a type can
// be retired without touching what it meant last year.
//
// Type is a free-form string written at submit time. There is no category
// collection and no lifecycle: which types exist, and when each was in
// use, are projections of the log rather than schema.
type Event struct {
	// ID is the store's identifier for this event, assigned on append. It
	// is empty on an event that has not been appended yet, and it is the
	// tiebreak that makes the load order total.
	ID string

	// Action is how Amount combines with the events before it.
	Action Action

	// Month is the calendar month the amount belongs to, "YYYY-MM". It is
	// the period being tracked, not when the event was written — an event
	// recorded today can correct last March.
	Month string

	// Type is the free-form category, as the user typed it: "Groceries",
	// "Mortgage". Compared verbatim, so "Rent" and "rent" are two types.
	Type string

	// Amount is the money, in integer cents. Negative is allowed and
	// meaningful: an add of a negative amount is how an overstatement is
	// walked back.
	Amount money.Cents

	// Direction is which way the money moved. The zero value normalizes
	// to DirectionExpense.
	Direction Direction

	// Note is an optional free-form remark — typically why a correction
	// was made, since the event itself only shows what changed.
	Note string

	// RefEventID optionally names the event this one corrects or
	// supersedes. It is provenance for a human reading the log: the fold
	// does not follow it, because an event's effect is decided by its own
	// action, not by what it points at.
	RefEventID string

	// RecordedAt is when the event was written, in UTC, to microsecond
	// resolution. It is the primary sort key of the log. A store assigns
	// it on append when it is zero; the importer sets it explicitly, so
	// that replayed history keeps the order it originally happened in.
	RecordedAt time.Time
}

// Normalize returns a copy of e with the conventions of the log applied:
// a zero Direction becomes DirectionExpense, Type and Note lose their
// surrounding whitespace, and RecordedAt is truncated to the log's
// resolution and moved to UTC.
//
// It is applied on the way into a store and on the way back out, so a
// document written before a default existed — by an older build, or by
// hand — reads back the same way a fresh one does.
//
// Trimming Type matters more than it looks: types are compared verbatim,
// so without it "Groceries " would be a second type that shadows
// "Groceries" in every month it appears in, and no edit could merge them.
func (e Event) Normalize() Event {
	if e.Direction == "" {
		e.Direction = DirectionExpense
	}
	e.Type = strings.TrimSpace(e.Type)
	e.Note = strings.TrimSpace(e.Note)
	if !e.RecordedAt.IsZero() {
		e.RecordedAt = e.RecordedAt.UTC().Truncate(timeResolution)
	}
	return e
}

// Validate reports whether e is a well-formed event, wrapping
// ErrInvalidEvent with the field at fault.
//
// It is what stands between a typo and a permanent entry in an
// append-only log: a bad event cannot be fixed in place once written, so
// it is refused before it is written. Validate expects a normalized event
// — a store normalizes first — and so requires the fields a default would
// have filled.
func (e Event) Validate() error {
	if !e.Action.valid() {
		return fmt.Errorf("%w: action %q is not one of %q, %q", ErrInvalidEvent, e.Action, ActionAdd, ActionSet)
	}
	if _, err := time.Parse(monthLayout, e.Month); err != nil {
		return fmt.Errorf("%w: month %q is not a calendar month %q", ErrInvalidEvent, e.Month, monthLayout)
	}
	if e.Type == "" {
		return fmt.Errorf("%w: type is empty", ErrInvalidEvent)
	}
	if !e.Direction.Valid() {
		return fmt.Errorf("%w: direction %q is not one of %q, %q", ErrInvalidEvent, e.Direction, DirectionExpense, DirectionIncome)
	}
	if e.RecordedAt.IsZero() {
		return fmt.Errorf("%w: recordedAt is zero", ErrInvalidEvent)
	}
	return nil
}

// valid reports whether the action is one the fold knows how to apply.
// Unknown actions are refused at the door rather than ignored at fold
// time: an event nothing folds is an amount the user entered and the
// totals silently omit.
func (a Action) valid() bool {
	switch a {
	case ActionAdd, ActionSet:
		return true
	default:
		return false
	}
}

// Valid reports whether the direction is one the rollups know how to count.
// It expects a normalized direction, so the empty one is already an expense.
//
// It is exported because the fold needs the same answer Validate does, and a
// second copy of this switch living in the projection is a copy that can be
// forgotten when a direction is added — leaving a direction the log accepts
// and the totals quietly drop.
func (d Direction) Valid() bool {
	switch d {
	case DirectionExpense, DirectionIncome:
		return true
	default:
		return false
	}
}
