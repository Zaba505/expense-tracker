package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"dagger/expense-tracker/internal/dagger"
)

// The seed lives here, in the run configuration, rather than in a binary
// the app ships: sample data is a property of "how I run this locally",
// not of the product. Nothing writes it but this module, and this module
// only ever talks to an emulator it started itself — so seed data cannot
// reach the real event log, which matters because an append-only log has
// no undo.

// seedEvent is one document in the events collection. It mirrors the
// agreed event shape — an action to fold with, the month it belongs to, a
// free-form type, integer cents, and a direction.
//
// The `firestore` field names in internal/eventlog are the contract this
// has to match; nothing type-checks the two against each other, because
// this module cannot import the app's internal packages. A drift shows up
// as seeded documents that decode to zero values.
type seedEvent struct {
	// id is the document id. Fixed rather than auto-generated, which is
	// what makes seeding idempotent: the chain seeds on every run, and a
	// second run has to overwrite these documents rather than pile eight
	// more on top of them.
	id string

	action      string
	month       string
	eventType   string
	amountCents int64
	direction   string
}

// seedEvents is two months of a plausible log: recurring expenses that
// are `set` (the month's rent is a fact, not a running total), groceries
// that `add` up over the month, income, and a type that appears in one
// month and not the other — the thing the old spreadsheet could not do.
var seedEvents = []seedEvent{
	{id: "seed-0001", action: "set", month: "2026-05", eventType: "Rent", amountCents: 185_000, direction: "expense"},
	{id: "seed-0002", action: "add", month: "2026-05", eventType: "Groceries", amountCents: 8_432, direction: "expense"},
	{id: "seed-0003", action: "add", month: "2026-05", eventType: "Groceries", amountCents: 11_207, direction: "expense"},
	{id: "seed-0004", action: "set", month: "2026-05", eventType: "Paycheck", amountCents: 520_000, direction: "income"},
	{id: "seed-0005", action: "set", month: "2026-06", eventType: "Rent", amountCents: 185_000, direction: "expense"},
	{id: "seed-0006", action: "add", month: "2026-06", eventType: "Groceries", amountCents: 9_915, direction: "expense"},
	{id: "seed-0007", action: "add", month: "2026-06", eventType: "Car repair", amountCents: 47_500, direction: "expense"},
	{id: "seed-0008", action: "set", month: "2026-06", eventType: "Paycheck", amountCents: 520_000, direction: "income"},
}

// seed writes the sample event log into a running emulator.
//
// It talks to the emulator straight from the module, over its REST API —
// no seeder container, no seeder binary. That works because a service
// with no custom hostname registers in the session's DNS domain, which
// module code can resolve; giving the emulator a WithHostname would break
// this (and is the bug behind z5labs/devex#147).
//
// emulator must already be started: Endpoint is the address of a running
// service, and starting it is also what pins the instance these documents
// land in (see RunAgainst.Local).
func seed(ctx context.Context, emulator *dagger.Service, project string) error {
	endpoint, err := emulator.Endpoint(ctx, dagger.ServiceEndpointOpts{Scheme: "http"})
	if err != nil {
		return fmt.Errorf("emulator endpoint: %w", err)
	}

	// One timestamp for the whole batch, so re-seeding rewrites the same
	// eight documents into a consistent state rather than a smear.
	now := time.Now().UTC().Format(time.RFC3339Nano)

	client := &http.Client{Timeout: 30 * time.Second}
	for _, event := range seedEvents {
		if err := writeEvent(ctx, client, endpoint, project, event, now); err != nil {
			return fmt.Errorf("seed event %s: %w", event.id, err)
		}
	}
	return nil
}

// writeEvent PATCHes one document into the events collection. PATCH, not
// POST: POST to a collection creates, and creating a document that is
// already there is an error — PATCH on the document path is the upsert
// that makes re-seeding idempotent.
func writeEvent(ctx context.Context, client *http.Client, endpoint, project string, event seedEvent, now string) error {
	url := fmt.Sprintf("%s/v1/projects/%s/databases/(default)/documents/%s/%s",
		endpoint, project, eventsCollection, event.id)

	// Firestore's REST wire form: every value carries its type.
	body, err := json.Marshal(map[string]any{
		"fields": map[string]any{
			"action":       map[string]any{"stringValue": event.action},
			"month":        map[string]any{"stringValue": event.month},
			"type":         map[string]any{"stringValue": event.eventType},
			"amount_cents": map[string]any{"integerValue": strconv.FormatInt(event.amountCents, 10)},
			"direction":    map[string]any{"stringValue": event.direction},
			"created_at":   map[string]any{"timestampValue": now},
		},
	})
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("write to %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("emulator returned %s: %s", resp.Status, bytes.TrimSpace(detail))
	}
	return nil
}
