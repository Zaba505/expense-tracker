// Command importer translates the owner's exported expense spreadsheet
// into an event stream appended to the event log.
//
// At this scaffolding stage it is a no-op entry point; the CSV parser and
// event-emission logic are added by a later story (`story(import)`).
package main

import (
	"log/slog"
	"os"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	logger.Info("importer scaffold: not yet implemented")
	// TODO(story:import): parse the sheet CSV and append events.
}
