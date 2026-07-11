// Command seed fills a local Firestore emulator with a few events, so a
// freshly started emulator is something to look at rather than an empty
// database.
//
// It is development tooling: it refuses to run unless
// FIRESTORE_EMULATOR_HOST is set, so it can never write sample data into
// the real event log — an append-only log has no undo.
//
// Re-running it is safe. The seed documents have fixed ids, so a second
// run overwrites the same documents instead of appending duplicates.
//
//	dagger call emulator up --ports 8085:8085          # in one shell
//	FIRESTORE_EMULATOR_HOST=localhost:8085 \
//	  GCP_PROJECT=demo-expense-tracker go run ./cmd/seed
//
// The document shape below is provisional: the Event type and the append
// path land with `story(eventlog)`, and this seeder moves onto them then.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/Zaba505/expense-tracker/internal/eventlog"
)

// seedEvent is one document in the events collection. It mirrors the
// agreed event shape — an action to fold with, the month it belongs to, a
// free-form type, integer cents, and a direction — without pinning it in
// a domain type ahead of the story that owns it.
type seedEvent struct {
	// ID is the document id, not a field of the document: fixed ids are
	// what make re-seeding overwrite rather than duplicate.
	ID string `firestore:"-"`

	Action      string    `firestore:"action"`
	Month       string    `firestore:"month"`
	Type        string    `firestore:"type"`
	AmountCents int64     `firestore:"amount_cents"`
	Direction   string    `firestore:"direction"`
	CreatedAt   time.Time `firestore:"created_at"`
}

// seedEvents is two months of a plausible log: recurring expenses that
// are `set` (the month's rent is a fact, not a running total), groceries
// that `add` up over the month, income, and a type that appears in one
// month and not the other — the thing the old spreadsheet could not do.
func seedEvents(now time.Time) []seedEvent {
	events := []seedEvent{
		{ID: "seed-0001", Action: "set", Month: "2026-05", Type: "Rent", AmountCents: 185_000, Direction: "expense"},
		{ID: "seed-0002", Action: "add", Month: "2026-05", Type: "Groceries", AmountCents: 8_432, Direction: "expense"},
		{ID: "seed-0003", Action: "add", Month: "2026-05", Type: "Groceries", AmountCents: 11_207, Direction: "expense"},
		{ID: "seed-0004", Action: "set", Month: "2026-05", Type: "Paycheck", AmountCents: 520_000, Direction: "income"},
		{ID: "seed-0005", Action: "set", Month: "2026-06", Type: "Rent", AmountCents: 185_000, Direction: "expense"},
		{ID: "seed-0006", Action: "add", Month: "2026-06", Type: "Groceries", AmountCents: 9_915, Direction: "expense"},
		{ID: "seed-0007", Action: "add", Month: "2026-06", Type: "Car repair", AmountCents: 47_500, Direction: "expense"},
		{ID: "seed-0008", Action: "set", Month: "2026-06", Type: "Paycheck", AmountCents: 520_000, Direction: "income"},
	}

	for i := range events {
		events[i].CreatedAt = now
	}
	return events
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	if err := run(context.Background(), logger); err != nil {
		logger.Error("seed failed", slog.Any("error", err))
		os.Exit(1)
	}
}

func run(ctx context.Context, logger *slog.Logger) error {
	// Deliberately not config.Load: the seeder needs neither PORT nor
	// OWNER_EMAIL, and — unlike the server — the emulator host is required
	// rather than optional. That requirement is the guard rail.
	emulatorHost := os.Getenv("FIRESTORE_EMULATOR_HOST")
	if emulatorHost == "" {
		return errors.New("FIRESTORE_EMULATOR_HOST is not set: seed only ever writes to a local emulator, never to the real event log")
	}
	projectID := os.Getenv("GCP_PROJECT")
	if projectID == "" {
		return errors.New("GCP_PROJECT is not set")
	}

	store, err := eventlog.New(ctx, eventlog.Options{
		ProjectID:    projectID,
		EmulatorHost: emulatorHost,
	})
	if err != nil {
		return err
	}
	defer func() {
		if err := store.Close(); err != nil {
			logger.Error("closing event store", slog.Any("error", err))
		}
	}()

	events := seedEvents(time.Now().UTC())
	for _, event := range events {
		if _, err := store.Events().Doc(event.ID).Set(ctx, event); err != nil {
			return fmt.Errorf("write event %s: %w", event.ID, err)
		}
	}

	logger.Info("seeded event log",
		slog.String("emulator", emulatorHost),
		slog.String("project", projectID),
		slog.Int("events", len(events)),
	)
	return nil
}
