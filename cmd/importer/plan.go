package main

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/Zaba505/expense-tracker/internal/domain"
	"github.com/Zaba505/expense-tracker/internal/projection"
)

// plan is what an import would do to the log, worked out before any of it
// is done.
//
// The importer decides everything first and writes second, and the reason
// is that the log is append-only: there is no run to take back. A --dry-run
// is then not a second code path that approximates the real one — it is the
// real one, stopped before the appends. What the dry run reports is what the
// import would do, because it is the same plan the import would carry out.
type plan struct {
	// Source is where the rows came from, for the report to name.
	Source string

	// Rows is every source row, in file order, and what the importer makes
	// of it.
	Rows []planned

	// Pending, Imported, Divergent, and Conflicting are the rows of each
	// outcome.
	Pending     []planned
	Imported    []planned
	Divergent   []planned
	Conflicting []planned

	// Months and Types are what the file turned out to contain, sorted and
	// deduplicated: the discovery half of a dry run, and the answer to "does
	// this file hold what I think it holds" before it is imported.
	Months []string
	Types  []string

	// Rollups is what the log folds to once Pending is appended: the whole
	// log, not just this file's rows, since the fold does not care where an
	// event came from. It is what the sheet's own rollup columns are checked
	// against — the tracker recomputes them from the log rather than storing
	// them, so the comparison is against the fold and not against a number
	// that was copied out of it.
	Rollups []projection.MonthRollup
}

// planned is one source row and what the importer will do with it.
type planned struct {
	// Row is the row's one-based position in the file, so a report names the
	// row a reader can count to.
	Row int

	// Key is the row's idempotency key, and the ID its event is appended
	// under.
	Key string

	// Event is the row, translated.
	Event domain.Event

	// Stored is the event already in the log that this row ran into: the
	// importer's own earlier import of the cell (outcomeImported,
	// outcomeDivergent), or the app's entry for it (outcomeConflict). Its
	// Amount is what the log says the cell came to, and Event.Amount is what
	// the sheet says.
	Stored domain.Event

	// Outcome is what the importer will do about it.
	Outcome outcome
}

// outcome is what an import does with one source row.
type outcome int

const (
	// outcomePending is a row the log has never seen. It will be appended.
	outcomePending outcome = iota

	// outcomeImported is a row already in the log, saying what the sheet
	// still says. It is skipped, and it is the outcome a re-run of an
	// unchanged file produces for every row in it — the ordinary case, not
	// an exceptional one.
	outcomeImported

	// outcomeDivergent is a row already in the log, saying something the
	// sheet no longer says: the figure was edited after it was imported.
	//
	// Nothing is written for it. The importer will not append a second total
	// for a cell that already has one, because a replayed row is dated at the
	// month it belongs to rather than at the moment it was read, so the two
	// would tie and the fold would pick between them by ID — which is to say
	// arbitrarily. Correcting an amount is the app's job, where a correction
	// is dated when it is made and therefore lands last, on purpose.
	outcomeDivergent

	// outcomeConflict is a row the importer has never imported, whose cell the
	// app has already recorded — and recorded early enough that importing the
	// row would bury it.
	//
	// "Corrections made in the app land last" is true because a replayed row
	// is dated at the first instant of the month it belongs to, and an entry
	// made during or after that month is later than that. It stops being true
	// the moment an entry is made *before* its month begins, which the app
	// permits and the owner has reason to do: next month's rent is knowable in
	// advance, and the month view navigates forward without limit. Such an
	// entry is dated earlier than the imported set, so the set would supersede
	// it — the owner's own figure, silently replaced by the sheet's, in a log
	// with no undo. The clamp does the same to a future month: a row for a
	// month that has not arrived is dated at the import, which is after any
	// entry made before it.
	//
	// So the rule is stated as a rule rather than inferred from the calendar:
	// an imported row is only appended when it would sort strictly before
	// every event the log already has for its cell. When it would not, nothing
	// is written and the row is reported. That covers the tie as well, which
	// the fold would otherwise settle by ID — arbitrarily.
	outcomeConflict
)

// makePlan works out what importing events into a log holding stored would
// do.
//
// stored is the whole log, in the log's order, and events are the file's
// rows in the file's order. Both are already in memory: the log is a single
// user's, the file is a single spreadsheet, and folding needs all of the
// former anyway.
func makePlan(source string, events, stored []domain.Event) (*plan, error) {
	// The log's events by ID, which for an imported event is its key. This is
	// the whole record of what has been imported: there is no ledger beside
	// the log to consult, and so nothing that can fall out of step with it.
	// An event the app appended has a random ID that no key can equal, so it
	// is simply never looked up.
	byKey := make(map[string]domain.Event, len(stored))
	for _, e := range stored {
		byKey[e.ID] = e
	}

	// And the last event the log holds for each cell, whoever wrote it. This
	// is the half that sees the app: an entry the owner typed has an ID the
	// store invented, so byKey never finds it, and without this index an
	// imported row would be appended straight over the top of one. See
	// outcomeConflict.
	latest := latestByCell(stored)

	p := &plan{Source: source, Rows: make([]planned, 0, len(events))}

	months := make(map[string]struct{})
	types := make(map[string]struct{})

	for i, e := range events {
		row := planned{Row: i + 1, Key: importKey(e), Event: e}

		months[e.Month] = struct{}{}
		types[e.Type] = struct{}{}

		prior, imported := byKey[row.Key]
		switch {
		case imported && prior.Amount == e.Amount:
			row.Stored = prior
			row.Outcome = outcomeImported
		case imported:
			row.Stored = prior
			row.Outcome = outcomeDivergent
		default:
			row.Outcome = outcomePending

			// Only a row that would land last is a row that would bury what is
			// under it, and only a row nothing is under can be appended
			// blindly. Not-Before rather than After, so a tie counts: two
			// events at one instant are ordered by ID, which is arbitrary.
			if last, found := latest[cellOf(e)]; found && !e.RecordedAt.Before(last.RecordedAt) {
				row.Stored = last
				row.Outcome = outcomeConflict
			}
		}

		p.Rows = append(p.Rows, row)
	}

	// Indexed after the loop rather than during it, so that nothing is copied
	// out of a Rows that is still growing.
	for _, row := range p.Rows {
		switch row.Outcome {
		case outcomePending:
			p.Pending = append(p.Pending, row)
		case outcomeImported:
			p.Imported = append(p.Imported, row)
		case outcomeDivergent:
			p.Divergent = append(p.Divergent, row)
		case outcomeConflict:
			p.Conflicting = append(p.Conflicting, row)
		}
	}

	p.Months = sortedKeys(months)
	p.Types = sortedKeys(types)

	rollups, err := rollupsAfter(stored, p.Pending)
	if err != nil {
		return nil, err
	}
	p.Rollups = rollups

	return p, nil
}

// cellOf is the cell an event belongs to, keyed the way the fold keys it, so
// that the importer and the month view mean the same thing by "a cell".
func cellOf(e domain.Event) projection.Key {
	return projection.Key{Month: e.Month, Type: e.Type, Direction: e.Direction}
}

// latestByCell is the last event the log holds for each cell, in the log's
// order — the one an imported row would have to sort ahead of to be safe.
//
// Only the actions that carry an amount are indexed. A rename moves no money
// and belongs to no cell; it changes the name a cell is read under, which is a
// fold-time question and not one an import has to answer. Nothing is lost by
// ignoring it here: an imported row that lands on a renamed-away type is a row
// whose cell has no events, which is exactly what this says about it.
//
// stored arrives in the log's order (Load's promise), so the last event seen
// for a cell is the last event in it. Sorting again would be re-deriving what
// the store already guarantees.
func latestByCell(stored []domain.Event) map[projection.Key]domain.Event {
	latest := make(map[projection.Key]domain.Event)
	for _, e := range stored {
		switch e.Action {
		case domain.ActionAdd, domain.ActionSet:
			latest[cellOf(e)] = e
		}
	}
	return latest
}

// rollupsAfter folds the log as it will be once pending is appended, and
// summarizes it by month.
//
// It is the check the story asks for, and it is a fold rather than a
// comparison against anything stored, because the tracker has no stored
// rollups to compare against: `Monthly Bills Total`, `Income` and `Savings`
// were formulas in the sheet, and here they are recomputed from the events
// every time they are shown. So the honest way to ask "did the import land
// what the sheet said" is to fold the log and read the months back — which
// is exactly what the app will do when it renders them.
//
// The events are sorted into the log's order before folding, because Fold
// applies what it is given in the order it is given it. Sorting them the way
// a store does — RecordedAt, then ID — is what makes a dry run's answer the
// answer: the pending events already know the IDs they will be appended
// under, since their IDs are their keys, so nothing about this fold is a
// guess about what the database would have chosen.
func rollupsAfter(stored []domain.Event, pending []planned) ([]projection.MonthRollup, error) {
	folding := make([]domain.Event, 0, len(stored)+len(pending))
	folding = append(folding, stored...)
	for _, row := range pending {
		e := row.Event
		e.ID = row.Key
		folding = append(folding, e)
	}

	slices.SortFunc(folding, func(a, b domain.Event) int {
		if c := a.RecordedAt.Compare(b.RecordedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})

	state, err := projection.Fold(folding)
	if err != nil {
		return nil, fmt.Errorf("importer: folding the log: %w", err)
	}

	rollups, err := projection.RollupByMonth(state)
	if err != nil {
		return nil, fmt.Errorf("importer: rolling up the log: %w", err)
	}
	return rollups, nil
}

// sortedKeys is a set as a sorted slice. Months sort chronologically for
// free — "YYYY-MM" is the layout domain chose so that they would.
func sortedKeys(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
