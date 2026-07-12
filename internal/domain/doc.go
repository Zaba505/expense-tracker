// Package domain defines the core event-sourcing types: the immutable
// Event (action, month, type, amount, direction, ...) that is the single
// source of truth, plus the vocabulary its actions and directions draw
// from.
//
// It is the one package that depends on nothing outside itself — no
// Firestore, no HTTP, no clock it is not handed. An event means the same
// thing here as it does in the database, and the rules for what makes one
// well-formed live with the type rather than with whatever happens to be
// writing it that day.
//
// The store that appends and loads events is internal/eventlog; the folds
// that turn them into what the user sees are internal/projection.
package domain
