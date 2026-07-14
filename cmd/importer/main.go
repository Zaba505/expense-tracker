// Command importer translates the owner's exported expense spreadsheet into an
// event stream appended to the event log.
//
// Its input is Parquet, not the spreadsheet's own CSV export. A conversion
// script — the owner's, ad hoc, run once — unpivots the sheet into one row per
// event and says what each row is. That is the thing the spreadsheet cannot say
// for itself: a column heading does not tell the importer whether the number
// under it is a bill, a paycheck, or a formula, so a wide export leaves it
// guessing, silently, into a log that cannot be edited afterwards. Parquet moves
// the unpivoting and the classification into the script, where the sheet's
// context actually is, and leaves the importer a schema it can check.
//
// The input schema, one row per event:
//
//	month         STRING  the calendar month, zero-padded: "2026-01"
//	type          STRING  the type, as it should read: "Rent"
//	amount_cents  INT64   the amount, in whole cents: 120000 is $1,200.00
//	direction     STRING  "expense" or "income"
//
// All four are required. Columns beyond them are ignored, so a script is free to
// carry provenance. Rollup and formula columns are not events and simply have no
// rows — there is nothing here for the importer to ignore, and so nothing for it
// to get wrong. Every row becomes a set event, because a spreadsheet cell is a
// total rather than a transaction.
//
// amount_cents is an integer for a reason worth stating: dollars written as a
// float truncate into it a hundredfold light, and every value would still look
// like a plausible number of cents. The importer checks the column's type before
// it reads a row.
//
// At this stage the entry point is still a no-op; the parser exists, and reading
// the file and appending its events are wired by a later story.
package main

import (
	"log/slog"
	"os"

	// Same reason as cmd/server: this ships in a scratch image, which has
	// no CA certificates, and appending the imported events means TLS to
	// Firestore. Wired up now rather than when the import logic lands,
	// because a missing trust store does not fail at build time — it fails
	// at the first handshake, in whatever environment runs it first.
	_ "golang.org/x/crypto/x509roots/fallback"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	logger.Info("importer scaffold: not yet implemented")
	// TODO(story:import): read the parquet file and append the parsed events.
}
