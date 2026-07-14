package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/Zaba505/expense-tracker/internal/domain"
)

// keyPrefix marks the events the importer wrote.
//
// It is not decoration. The importer's keys and Firestore's own document
// IDs share one collection, and the importer decides whether it has
// already imported a row by looking for the row's key among the IDs the
// log yields. A prefix no random ID can have — Firestore's are twenty
// alphanumeric characters, with no dash — is what keeps that question from
// ever being answered by a coincidence, and it makes the collection
// readable to a human who opens it: an ID that starts with "import-" came
// from the sheet, and every other ID came from the app.
const keyPrefix = "import-"

// importKey is the idempotency key of a source row: the name its event is
// appended under, and the name a re-run looks for to know that it has
// already been imported.
//
// It names the cell the row came from — month, type, direction — and
// deliberately not the amount. Those three fields are exactly the ones
// projection.Key folds by, so one key is one cell of the sheet is one cell
// of the month view, and the importer's idea of a duplicate cannot drift
// from the fold's idea of a cell.
//
// Leaving the amount out is what makes a re-run safe rather than merely
// quiet. Were the amount in the key, editing a figure in the sheet and
// re-importing would mint a fresh key, and the row would land as a second
// set event for a cell that already had one — dated, as every replayed row
// is, at the first instant of the month it belongs to. Two set events, one
// cell, one timestamp: which of them the fold applies last would come down
// to which ID sorted higher, and the log's order says in as many words
// that nothing may read meaning into that. The month would come out at one
// of the two amounts, arbitrarily, and stay that way.
//
// With the amount out of the key, an edited row is instead recognized as
// the cell it already is, and the importer refuses to write it and says
// so. Correcting it is the app's job (a correction is dated when it is
// made, which is what puts it last), and the importer's job is to be the
// one thing that never argues with the log about the past.
//
// The event is normalized before it gets here, so a type that was typed
// with a trailing space keys the same as one that was not — the same rule
// that keeps "Groceries " from shadowing "Groceries" in the fold.
func importKey(e domain.Event) string {
	sum := sha256.Sum256(keyMaterial(e))
	return keyPrefix + hex.EncodeToString(sum[:])
}

// keyMaterial is the byte string importKey hashes: the three fields, each
// preceded by its length.
//
// The lengths are what make it unambiguous. A plain separator only works
// while no field can contain it, and a type is whatever the sheet's column
// heading happened to be — free-form text this package does not get to put
// rules on. Length-prefixing means no two distinct cells can produce the
// same bytes whatever they are spelled with, so no two distinct cells can
// collide onto one key and have the second silently read as an import of
// the first.
func keyMaterial(e domain.Event) []byte {
	return fmt.Appendf(nil, "%d:%s%d:%s%d:%s",
		len(e.Month), e.Month,
		len(e.Type), e.Type,
		len(e.Direction), e.Direction,
	)
}
