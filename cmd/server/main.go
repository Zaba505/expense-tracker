// Command server runs the expense-tracker HTTP application.
//
// It loads its configuration from the environment (see internal/config),
// connects to Firestore — the live service, or a local emulator when
// FIRESTORE_EMULATOR_HOST is set — serves on $PORT, and shuts down
// gracefully when the platform sends SIGTERM.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	// The z5labs pipeline ships this binary in a scratch image, which
	// carries no CA certificates. Firestore is reached over TLS, so
	// without a trust store the first call fails with "certificate signed
	// by unknown authority". This embeds Mozilla's roots into the binary,
	// which is what makes the scratch image viable.
	_ "golang.org/x/crypto/x509roots/fallback"

	"github.com/Zaba505/expense-tracker/internal/auth"
	"github.com/Zaba505/expense-tracker/internal/config"
	"github.com/Zaba505/expense-tracker/internal/eventlog"
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

	// Connecting is lazy — this fails only on a configuration or
	// credentials problem, which is worth dying for. Whether Firestore is
	// actually reachable is answered by /health/readiness, not by
	// refusing to boot: an instance that cannot reach the database should
	// come up and say so.
	store, err := eventlog.New(ctx, eventlog.Options{
		ProjectID:    cfg.GCPProject,
		EmulatorHost: cfg.FirestoreEmulatorHost,
	})
	if err != nil {
		return err
	}
	defer func() {
		if err := store.Close(); err != nil {
			logger.Error("closing event store", slog.Any("error", err))
		}
	}()

	// Like the event store, this reaches no network: Google's signing keys
	// are fetched at the first sign-in, not at boot, so accounts.google.com
	// having a bad minute cannot stop a revision from rolling out.
	authn, err := auth.New(ctx, auth.Config{
		ClientID:     cfg.OAuthClientID,
		ClientSecret: cfg.OAuthClientSecret,
		SessionKey:   cfg.SessionKey,
		BaseURL:      cfg.BaseURL,
		Logger:       logger,
	})
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

	return web.Serve(ctx, lis, web.NewHandler(logger, store, authn), logger)
}
