// Command importer translates the owner's exported expense spreadsheet
// into an event stream appended to the event log.
//
// At this scaffolding stage it is a no-op entry point; the CSV parser and
// event-emission logic are added by a later story (`story(import)`).
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
	// TODO(story:import): parse the sheet CSV and append events.
}
