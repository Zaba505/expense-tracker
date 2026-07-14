package main

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/parquet-go/parquet-go"

	"github.com/Zaba505/expense-tracker/internal/domain"
	"github.com/Zaba505/expense-tracker/internal/money"
)

// row is one row of the importer's input, and one event.
//
// The input is Parquet rather than the sheet's CSV export, and it is long
// rather than wide: the owner's conversion script unpivots the spreadsheet,
// so a row carries the type it is for and the direction the money moved,
// instead of the importer having to infer either from a column heading.
//
// That is what keeps this file free of a derived-column denylist. A wide
// sheet forces the importer to decide, for every column it has never seen,
// whether the number under it is a bill or a formula — and to guess wrong
// silently, into a log that cannot be edited. Here the script simply does
// not emit a row for a rollup column, because a rollup is not an event.
type row struct {
	Month string `parquet:"month"`
	Type  string `parquet:"type"`

	// A pointer, so that a NULL can be told from a zero.
	//
	// They are the same int64 otherwise, and they are not remotely the same
	// fact: a zero is a cell the owner zeroed out, and a NULL is a cell the
	// conversion script had nothing to say about. Decoded into a plain int64
	// they are both 0, and the row would import as a set of $0.00 — a month
	// where that type cost nothing, permanently, in a log with no undo, and
	// indistinguishable from a deliberate zeroing.
	//
	// It is not a hypothetical: a nullable INT64 is what pandas and pyarrow
	// write for any column with a gap in it, and it has the same
	// parquet.Kind as a required one, so the schema check waves it through.
	// The other three columns escape this only by luck — a NULL decodes them
	// to "", which the per-row checks already refuse — and the amount is the
	// one field with no illegal value to fall back on.
	//
	// A pointer reads both shapes: an optional column yields nil for a NULL,
	// and a required column always yields a value. So the tolerant thing and
	// the safe thing are the same thing — the file a script writes by
	// default is accepted, and only the rows that are actually missing an
	// amount are refused.
	AmountCents *int64 `parquet:"amount_cents"`

	Direction string `parquet:"direction"`
}

// inputColumns is the schema the input must have, checked before a single
// row is decoded.
//
// parquet-go will not check it for us, and its defaults are the dangerous
// ones: a column the file omits decodes as the zero value, and a column
// whose type is wrong is coerced rather than refused. Three ways that ends
// badly, none of which a per-row check can catch afterwards:
//
//   - No direction column at all decodes as "", which Normalize reads as an
//     expense — quietly filing every paycheck of the last five years as a bill.
//   - amount_cents written as a float of dollars (1200.00, the natural thing
//     for a pandas script to do) truncates into an int64 as 1200. Every amount
//     lands a hundredfold light, and every one of them is still a plausible
//     number of cents, so nothing downstream can tell.
//   - direction written as an int decodes as its digits ("7"), which at least
//     fails the per-row check — but only by luck.
//
// The log is append-only, so each of these is permanent the moment it is
// appended. The schema is therefore checked, not trusted, and the amount is
// required to be an integer count of cents — the one representation that
// cannot quietly lose a factor of a hundred.
//
// What this check deliberately does not do is insist the columns be required
// rather than optional. A nullable column has the same Kind as a required one
// and would pass here regardless, and refusing every optional column would
// refuse the file pandas writes by default. The gap that leaves — a NULL
// amount, which decodes as a zero and would import as a set of $0.00 — is
// closed a row at a time instead. See row.AmountCents.
var inputColumns = []struct {
	name string
	kind parquet.Kind
}{
	{"month", parquet.ByteArray},
	{"type", parquet.ByteArray},
	{"amount_cents", parquet.Int64},
	{"direction", parquet.ByteArray},
}

// rowError reports which input row could not be translated, and which of
// its columns was at fault.
type rowError struct {
	Row    int
	Column string
	Err    error
}

func (e *rowError) Error() string {
	return fmt.Sprintf("importer: row %d (%s): %v", e.Row, e.Column, e.Err)
}

func (e *rowError) Unwrap() error { return e.Err }

// duplicateCellError reports two rows for the same cell of the sheet.
type duplicateCellError struct {
	First, Second int
	Event         domain.Event
}

func (e *duplicateCellError) Error() string {
	return fmt.Sprintf("importer: rows %d and %d are both the cell %s / %s / %s; a cell is one event, and a sheet with two totals for one cell does not say which of them is the total",
		e.First, e.Second, e.Event.Month, e.Event.Type, e.Event.Direction)
}

// parseParquet translates the converted sheet into the events to import.
//
// now is handed in rather than read from the clock so that the clamp in
// recordedAt is testable, and for the reason internal/domain gives for
// taking no clock it was not given.
func parseParquet(r io.ReaderAt, size int64, now time.Time) ([]domain.Event, error) {
	f, err := parquet.OpenFile(r, size)
	if err != nil {
		return nil, fmt.Errorf("importer: open parquet: %w", err)
	}
	if err := checkSchema(f.Schema()); err != nil {
		return nil, err
	}

	rows, err := parquet.Read[row](r, size)
	if err != nil {
		return nil, fmt.Errorf("importer: read parquet: %w", err)
	}

	// The row each cell of the sheet was last seen at. Two rows for one cell
	// is a file the importer cannot honor: both would become set events for
	// the same month and type, so the second would supersede the first — and
	// a set replayed from a sheet is dated at the month it belongs to, not at
	// the moment it was read, so the two would land at one instant and the
	// fold would apply them in whichever order their IDs happened to sort. A
	// number the owner never chose would be the one the month came out at.
	//
	// It is also, mechanically, the one file the append could not carry out:
	// one cell is one key, and the log takes a key once.
	cells := make(map[string]int, len(rows))

	events := make([]domain.Event, 0, len(rows))
	for i, in := range rows {
		// One-based, so a number here names the same row the script's own
		// diagnostics do, and the row a reader counts to in a viewer.
		number := i + 1

		if !domain.ValidMonth(in.Month) {
			return nil, &rowError{Row: number, Column: "month", Err: fmt.Errorf("invalid month %q", in.Month)}
		}

		// A row with no amount at all. Refused rather than read as zero,
		// because zero is a legal amount — a set of zero is how a type is
		// zeroed out for a month — so there is no value the domain could
		// reject on this one's behalf. It has to be caught here or not at
		// all. See row.AmountCents.
		if in.AmountCents == nil {
			return nil, &rowError{Row: number, Column: "amount_cents", Err: errors.New("the row has no amount; a blank is not a zero")}
		}

		// Checked before Normalize, which is the whole point: Normalize
		// reads the empty direction as an expense, so a row that never said
		// which way the money moved would import as a bill without a word.
		// An import is not an entry form — nothing here gets to default.
		if !domain.Direction(in.Direction).Valid() {
			return nil, &rowError{
				Row:    number,
				Column: "direction",
				Err:    fmt.Errorf("direction %q is not one of %q, %q", in.Direction, domain.DirectionExpense, domain.DirectionIncome),
			}
		}

		event := domain.Event{
			Action:     domain.ActionSet,
			Month:      in.Month,
			Type:       in.Type,
			Amount:     money.Cents(*in.AmountCents),
			Direction:  domain.Direction(in.Direction),
			RecordedAt: recordedAt(in.Month, now, i),
		}.Normalize()

		if err := event.Validate(); err != nil {
			return nil, &rowError{Row: number, Column: columnAtFault(event), Err: err}
		}

		// Keyed off the normalized event, so a type written with a trailing
		// space is the same cell as one without — which is what the fold will
		// make of them, and the importer has to agree with the fold about what
		// a cell is or it will happily append two events the month view then
		// reads as one.
		key := importKey(event)
		if first, seen := cells[key]; seen {
			return nil, &duplicateCellError{First: first, Second: number, Event: event}
		}
		cells[key] = number

		events = append(events, event)
	}

	return events, nil
}

// checkSchema reports whether the file has the columns the importer requires,
// with the types it requires. Columns beyond them are ignored: a conversion
// script is free to carry provenance — which cell a row came from — and the
// importer has no opinion about it.
func checkSchema(schema *parquet.Schema) error {
	for _, want := range inputColumns {
		leaf, ok := schema.Lookup(want.name)
		if !ok {
			return fmt.Errorf("importer: input is missing the %q column", want.name)
		}
		if got := leaf.Node.Type().Kind(); got != want.kind {
			return fmt.Errorf("importer: column %q is %s, want %s", want.name, got, want.kind)
		}
	}
	return nil
}

// recordedAt dates an imported event, which decides what it can be corrected
// by. The log's order is RecordedAt then ID, and a set supersedes the sets
// before it, so the last event for a month and type is the one that counts.
//
// A month that has already happened is dated at its first instant. That is
// what makes the import history rather than news: an event replayed for
// January sorts before every correction made since, so a correction still
// wins, exactly as it would have if the log had been there all along.
//
// A month that has not happened yet is dated now instead. The sheet has rows
// for months ahead of today — next month's rent is already in it — and dating
// those at the month they name would stamp them in the future, ahead of any
// correction the owner makes before the month arrives. The import would then
// supersede an edit made after it, and the edit would vanish until the month
// came around. Clamping is what keeps "the last word wins" true in wall-clock
// terms rather than in spreadsheet terms.
//
// sequence is the row's position in the file, and separates events that would
// otherwise share an instant — the rows of one month, and every row clamped to
// now. It steps by the resolution the log keeps, so that Normalize's truncation
// cannot collapse two events back onto each other.
func recordedAt(month string, now time.Time, sequence int) time.Time {
	// Safe: the month was checked before this is reached.
	start, _ := domain.ParseMonth(month)
	if start.After(now) {
		start = now
	}
	return start.Add(time.Duration(sequence) * time.Microsecond)
}

// columnAtFault names the column behind a Validate failure, so that a row the
// domain refuses is reported the same way a row this file refuses is. Only the
// fields the input can actually get wrong are worth naming: the action and the
// timestamp are this file's to set, and a fault in either is a bug here rather
// than a bad row.
func columnAtFault(e domain.Event) string {
	switch {
	case !domain.ValidMonth(e.Month):
		return "month"
	case strings.TrimSpace(e.Type) == "":
		return "type"
	case !e.Direction.Valid():
		return "direction"
	default:
		return "row"
	}
}
