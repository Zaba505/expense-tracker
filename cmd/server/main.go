// Command server runs the expense-tracker HTTP application.
//
// At this scaffolding stage it loads and validates configuration, then
// exits. The HTTP server, routing, and Firestore wiring are added by
// later stories (`story(web)`, `story(eventlog)`).
package main

import (
	"log/slog"
	"os"

	"github.com/Zaba505/expense-tracker/internal/config"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		logger.Error("failed to load configuration", slog.Any("error", err))
		os.Exit(1)
	}

	logger.Info("configuration loaded",
		slog.String("addr", cfg.Addr()),
		slog.String("gcp_project", cfg.GCPProject),
		slog.Bool("firestore_emulator", cfg.UsesEmulator()),
	)
	// TODO(story:web): start the net/http server with graceful shutdown.
}
