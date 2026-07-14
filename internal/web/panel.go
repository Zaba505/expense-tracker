package web

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"

	"github.com/Zaba505/expense-tracker/internal/domain"
	"github.com/Zaba505/expense-tracker/internal/eventlog"
	"github.com/Zaba505/expense-tracker/internal/projection"
	"github.com/Zaba505/expense-tracker/internal/view"
)

// loadPanel builds the month panel: it replays the whole log and takes from it
// the one month being shown — that month's cells, that month's rollup — plus
// every type the log has ever mentioned, which is the form's autocomplete.
//
// It folds on every render, which is the bargain an event-sourced read model
// makes at this size: one owner's log is thousands of events, not millions, and
// a total recomputed from the events cannot drift from them the way a stored
// one can. Nothing is cached, so nothing has to be invalidated when an event is
// appended — the next render simply sees it. If this ever gets slow, the answer
// is a snapshot to fold forward from, not a running total kept up to date by
// hand: that would put the app back in the business of maintaining a number the
// log already knows.
//
// month decides which cells come back, and form is threaded through untouched:
// the panel is rendered both after an event was appended and after one was
// refused, and only the caller knows which.
func loadPanel(ctx context.Context, log eventlog.EventStore, month string, form view.Form) (view.Panel, error) {
	events, state, err := loadState(ctx, log)
	if err != nil {
		return view.Panel{}, err
	}

	rollups, err := projection.RollupByMonth(state)
	if err != nil {
		return view.Panel{}, fmt.Errorf("rolling up the log: %w", err)
	}

	panel := view.Panel{
		Month:      month,
		Rows:       rowsFor(state, month),
		Events:     eventsFor(events, month),
		KnownTypes: projection.KnownTypes(events),
		Form:       form,
	}

	// A month the log says nothing about keeps the zero Rollup, which is the
	// truth about it: nothing recorded is nothing spent. RollupByMonth
	// deliberately does not invent a row for it, so there is nothing to find.
	for _, rollup := range rollups {
		if rollup.Month == month {
			panel.Rollup = rollup.Rollup
			break
		}
	}

	return panel, nil
}

// loadState drains the log and folds it into the current state.
func loadState(ctx context.Context, log eventlog.EventStore) ([]domain.Event, projection.State, error) {
	events, err := loadEvents(ctx, log)
	if err != nil {
		return nil, nil, err
	}

	state, err := projection.Fold(events)
	if err != nil {
		return nil, nil, fmt.Errorf("folding the log: %w", err)
	}

	return events, state, nil
}

// loadEvents drains the log into a slice, in the log's order.
//
// The projections take a slice rather than a stream because two of them —
// Fold and KnownTypes — walk the same events, and KnownTypes walks them
// backwards. Loading once and folding twice beats streaming the log twice.
//
// The first error ends the load and is returned: a partially-read log is not a
// smaller log, it is a wrong one, and folding what arrived before the failure
// would render totals that quietly omit whatever did not.
func loadEvents(ctx context.Context, log eventlog.EventStore) ([]domain.Event, error) {
	var events []domain.Event
	for event, err := range log.Load(ctx) {
		if err != nil {
			return nil, fmt.Errorf("loading the log: %w", err)
		}
		events = append(events, event)
	}
	return events, nil
}

// rowsFor is the month's cells, in a stable order: by direction, then by type.
//
// The order is only required to be deterministic — two renders of an unchanged
// log must not shuffle the rows — and sorting by the direction string gives
// that for free, with expenses landing above income because "expense" precedes
// "income" in the alphabet. Nothing depends on that happening to be the useful
// order; a direction added later sorts wherever its name falls, and the table
// stays stable either way.
func rowsFor(state projection.State, month string) []view.Row {
	var rows []view.Row
	for key, amount := range state {
		if key.Month != month {
			continue
		}
		rows = append(rows, view.Row{
			Type:      key.Type,
			Direction: key.Direction,
			Amount:    amount,
		})
	}

	slices.SortFunc(rows, func(a, b view.Row) int {
		return cmp.Or(
			cmp.Compare(a.Direction, b.Direction),
			cmp.Compare(a.Type, b.Type),
		)
	})
	return rows
}

func eventsFor(events []domain.Event, month string) []domain.Event {
	var filtered []domain.Event
	for _, event := range events {
		if event.Month == month {
			filtered = append(filtered, event)
		}
	}
	return filtered
}

// renderPanel writes the panel as the whole response body, at the given status:
// 200 when an event was appended, 422 when the submission was refused.
//
// The status is the caller's because only the caller knows which of those
// happened, and it is the only part of the answer that says so — the body is
// the same panel either way. htmx is configured to swap a 422 (see
// view.htmxConfig), so a refusal reaches the page rather than being dropped as
// an error.
//
// A failed render is logged, not answered with a 500, for the reason handleHome
// gives: templ streams straight to the ResponseWriter, so by the time a render
// can fail the status line is already sent and http.Error would only append its
// text to a half-written fragment.
func renderPanel(w http.ResponseWriter, r *http.Request, logger *slog.Logger, panel view.Panel, status int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)

	if err := view.MonthPanel(panel).Render(r.Context(), w); err != nil {
		logger.ErrorContext(r.Context(), "rendering the month panel",
			slog.String("month", panel.Month),
			slog.Any("error", err),
		)
	}
}
