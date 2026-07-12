// Package eventlog is the Firestore-backed append-only event store: the
// single source of truth the whole app folds its read models out of.
// Events are written immutably — a correction is a new compensating
// event, never an edit — and loaded in deterministic order.
//
// One Store serves both environments unchanged: it reaches the live
// service with Application Default Credentials, or a local Firestore
// emulator when Options.EmulatorHost says so. Memory is the same log with
// nothing under it, for tests and folds that need no database.
//
// The interface both satisfy is EventStore, and it has no Update and no
// Delete. Consumers depend on that interface rather than on Firestore,
// which is what keeps the append-only rule from being something each
// caller has to remember.
package eventlog
