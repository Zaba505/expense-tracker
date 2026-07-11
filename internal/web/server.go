package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// ShutdownTimeout bounds how long Serve waits for in-flight requests to
// finish once shutdown starts. Cloud Run sends SIGTERM and kills the
// container ~10s later, so this stays under that: a request that has not
// finished by then would be cut off by the platform regardless.
const ShutdownTimeout = 8 * time.Second

// readHeaderTimeout caps how long a client may dawdle over its request
// headers, so a slow-loris connection cannot pin a goroutine forever.
const readHeaderTimeout = 10 * time.Second

// Serve serves h on lis until ctx is cancelled, then shuts down
// gracefully: it stops accepting connections and gives requests already in
// flight up to ShutdownTimeout to finish. It returns nil on a clean
// shutdown, and the reason otherwise — a failure to accept, or requests
// still running when the timeout expired.
//
// Serve owns lis and closes it before returning.
func Serve(ctx context.Context, lis net.Listener, h http.Handler, logger *slog.Logger) error {
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: readHeaderTimeout,
	}

	// Buffered: if Serve returns via the ctx.Done() path below, this send
	// must not block a goroutine forever waiting for a receiver.
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(lis) }()

	select {
	case err := <-serveErr:
		// The server stopped on its own, without being asked to.
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	logger.Info("shutting down", slog.Duration("grace_period", ShutdownTimeout))

	// Detached from ctx, which is already cancelled — this deadline is the
	// grace period, not zero.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}

	// Shutdown has returned, so Serve has already handed back
	// ErrServerClosed; take it off the channel and treat it as the clean
	// exit it is.
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}

	logger.Info("shutdown complete")
	return nil
}
