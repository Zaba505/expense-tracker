// Package eventlog is the Firestore-backed append-only event store: the
// single source of truth the whole app folds its read models out of.
// Events are written immutably — a correction is a new compensating
// event, never an edit — and loaded in deterministic order.
//
// One Store serves both environments unchanged: it reaches the live
// service with Application Default Credentials, or a local Firestore
// emulator when Options.EmulatorHost says so.
//
// This story wires up the client, the events collection, and the
// connectivity Check behind GET /health/readiness. The Event type and the
// append/load operations arrive with the next `story(eventlog)`.
package eventlog
