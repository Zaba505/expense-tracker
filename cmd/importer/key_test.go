package main

import (
	"strings"
	"testing"

	"github.com/Zaba505/expense-tracker/internal/domain"
	"github.com/Zaba505/expense-tracker/internal/eventlog"
	"github.com/Zaba505/expense-tracker/internal/projection"
)

// cell is a source row's identity, as the key sees it.
func cell(month, typ string, direction domain.Direction) domain.Event {
	return domain.Event{
		Action:    domain.ActionSet,
		Month:     month,
		Type:      typ,
		Amount:    120_000,
		Direction: direction,
	}
}

// TestImportKeyNamesTheCell is the property the whole import rests on: the key
// is the cell, so the same row always keys the same way and a different row
// never does. It is what makes a re-run skip what it already appended, and it
// is checked here rather than only through an import because a collision would
// show up there as an event quietly missing, not as a failure.
func TestImportKeyNamesTheCell(t *testing.T) {
	base := cell("2026-01", "Rent", domain.DirectionExpense)

	t.Run("the same cell keys the same, every time", func(t *testing.T) {
		// Built again from its parts rather than compared with itself: what a
		// re-run actually does is parse the file a second time, so the claim is
		// that a cell keys the same across two readings of it — not that one
		// value keys the same as itself.
		again := cell("2026-01", "Rent", domain.DirectionExpense)

		if importKey(again) != importKey(base) {
			t.Error("importKey is not deterministic; a re-run would import every row all over again")
		}
	})

	t.Run("a different cell keys differently", func(t *testing.T) {
		others := map[string]domain.Event{
			"another month":    cell("2026-02", "Rent", domain.DirectionExpense),
			"another type":     cell("2026-01", "Groceries", domain.DirectionExpense),
			"another direcion": cell("2026-01", "Rent", domain.DirectionIncome),
		}

		for name, other := range others {
			if importKey(other) == importKey(base) {
				t.Errorf("%s keys the same as %s / %s / %s; one of the two cells would never be imported",
					name, base.Month, base.Type, base.Direction)
			}
		}
	})

	t.Run("the amount is not part of the key", func(t *testing.T) {
		// The rule that makes an edited sheet a divergence to report rather
		// than a second set event to append. See importKey.
		edited := base
		edited.Amount = 999_999

		if importKey(edited) != importKey(base) {
			t.Error("editing the amount changed the key; a re-import would append a second total for a cell that already has one, and the fold would pick between them by ID")
		}
	})

	t.Run("a type is trimmed before it is keyed", func(t *testing.T) {
		// Normalize trims, and the fold keys by the trimmed type, so the
		// importer has to as well — otherwise "Rent " imports as a second cell
		// that the month view then shows as one with "Rent".
		spaced := base
		spaced.Type = "  Rent  "

		if importKey(spaced.Normalize()) != importKey(base) {
			t.Error("a type with surrounding whitespace keys differently from the trimmed one the fold will use")
		}
	})

	t.Run("no two fields can be smuggled into one", func(t *testing.T) {
		// The reason the key material is length-prefixed. A type is free-form
		// text from a spreadsheet heading, so it can contain whatever a
		// separator would be — and two cells that hashed the same would be one
		// cell to the importer, with the second silently never imported.
		clash := []domain.Event{
			cell("2026-01", "Rent", domain.DirectionExpense),
			cell("2026-01", "Rent1:", domain.DirectionExpense),
			cell("2026-0", "1Rent", domain.DirectionExpense),
			cell("2026-01", "Ren", domain.DirectionExpense),
		}

		seen := make(map[string]domain.Event)
		for _, e := range clash {
			key := importKey(e)
			if prior, ok := seen[key]; ok {
				t.Errorf("%s / %q and %s / %q share the key %s", e.Month, e.Type, prior.Month, prior.Type, key)
			}
			seen[key] = e
		}
	})
}

// TestImportKeyIsTheCellTheFoldKeysBy pins the two definitions of "a cell"
// together. The importer decides what a duplicate is; the projection decides
// what a month view row is. If they ever disagreed, the importer would append
// two events the app then shows as one — a total that is quietly the wrong one,
// in a log with no undo.
func TestImportKeyIsTheCellTheFoldKeysBy(t *testing.T) {
	events := []domain.Event{
		cell("2026-01", "Rent", domain.DirectionExpense),
		cell("2026-01", "Groceries", domain.DirectionExpense),
		cell("2026-01", "Income", domain.DirectionIncome),
		cell("2026-02", "Rent", domain.DirectionExpense),
	}

	state, err := projection.Fold(events)
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}

	keys := make(map[string]struct{}, len(events))
	for _, e := range events {
		keys[importKey(e)] = struct{}{}
	}

	// One key per folded cell, and one folded cell per key: the two partition
	// the same events the same way.
	if len(keys) != len(state) {
		t.Errorf("%d events produced %d import keys but %d folded cells; the importer and the fold disagree about what a cell is", len(events), len(keys), len(state))
	}
}

// TestImportKeyIsALegalDocumentID guards the join between this package and the
// store: the key becomes a Firestore document ID, and the store refuses one it
// cannot name a document with. A key that failed here would fail on the first
// row of a real import — after the file had parsed, and after the log had been
// read, which is a slow way to find out.
func TestImportKeyIsALegalDocumentID(t *testing.T) {
	// A type doing everything a spreadsheet heading might: a slash, which is
	// the character that would otherwise address a subcollection; the reserved
	// spelling; an emoji; something long.
	types := []string{
		"Rent",
		"Insurance / Auto",
		"__id7__",
		"Coffee ☕",
		strings.Repeat("Miscellaneous ", 200),
		"..",
	}

	log := eventlog.NewMemory()
	for _, typ := range types {
		e := cell("2026-01", typ, domain.DirectionExpense).Normalize()
		e.RecordedAt = importedAt

		if _, err := log.AppendUnique(t.Context(), importKey(e), e); err != nil {
			t.Errorf("AppendUnique with the key for the type %q: %v", typ, err)
		}
	}
}
