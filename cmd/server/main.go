// Command server runs the expense-tracker HTTP application.
//
// It loads its configuration from the environment (see internal/config),
// serves on $PORT, and shuts down gracefully when the platform sends
// SIGTERM. Firestore wiring arrives with `story(eventlog)`.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/Zaba505/expense-tracker/internal/config"
	"github.com/Zaba505/expense-tracker/internal/web"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	if err := run(context.Background(), logger); err != nil {
		logger.Error("server exited", slog.Any("error", err))
		os.Exit(1)
	}
}

// run is main's body with the process-level concerns (logger, exit code)
// lifted out, so every failure is a returned error rather than an
// os.Exit deep in the call stack.
func run(ctx context.Context, logger *slog.Logger) error {
	// Cloud Run signals a shutdown with SIGTERM; Ctrl-C locally is SIGINT.
	// Cancelling ctx is what tells web.Serve to start draining.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}

	// Listen before announcing: a port already in use should fail here,
	// not look like a healthy start followed by silence.
	lis, err := net.Listen("tcp", cfg.Addr())
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Addr(), err)
	}

	logger.Info("server listening",
		slog.String("addr", lis.Addr().String()),
		slog.String("gcp_project", cfg.GCPProject),
		slog.Bool("firestore_emulator", cfg.UsesEmulator()),
	)

	return web.Serve(ctx, lis, web.NewHandler(logger), logger)
}
