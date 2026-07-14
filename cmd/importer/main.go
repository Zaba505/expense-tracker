// Command importer translates the owner's exported expense spreadsheet
// into an event stream appended to the event log.
//
// At this stage the entry point is still a no-op; the CSV parser now exists,
// and appending the translated events is wired by a later story.
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
