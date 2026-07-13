// Package projection folds the event log into read models: monthly
// state, direction-based rollups, the known-types list, and the yearly
// grid. Everything the user sees is derived here rather than stored.
//
// Fold is pure and order-respecting: it applies events in the order it is
// given, and turns the current sequence into the current monthly state for
// each (month, type, direction) cell. Unknown or future actions fail the fold
// rather than being ignored, so the projection cannot quietly omit an event.
//
// RollupByMonth then sums that state by direction into what the sheet used to
// keep as formulas — expenses, income, net — and Total adds a span of months
// up into the totals below them. Both are pure functions of the state, so a
// handler folds the log once and renders the month and its totals from the
// same value, and no number the user reads can disagree with the events it
// came from. That is the point of the package: the old sheet's rollups were
// hand-maintained and could drift, and a formula that cannot be stored cannot
// drift.
package projection
