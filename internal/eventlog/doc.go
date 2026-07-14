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
//
// UniqueAppender is EventStore plus one thing: an append that carries the
// caller's own key for the fact it is appending, and refuses to append the
// same fact twice. It is a second interface because it has exactly one
// caller — the importer, replaying a spreadsheet it may have replayed
// before — and a handler that never gets handed it can never reach for it.
package eventlog
