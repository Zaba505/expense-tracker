// Package projection folds the event log into read models: monthly
// state, direction-based rollups, the known-types list, and the yearly
// grid. Everything the user sees is derived here rather than stored.
//
// Fold is pure and order-respecting: it applies events in the order it is
// given, and turns the current sequence into the current monthly state for
// each (month, type) pair. Unknown or future actions fail the fold rather
// than being ignored, so the projection cannot quietly omit an event.
package projection
