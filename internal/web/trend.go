package web

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Zaba505/expense-tracker/internal/eventlog"
	"github.com/Zaba505/expense-tracker/internal/projection"
	"github.com/Zaba505/expense-tracker/internal/view"
)

// handleTrend renders one type's month-by-month history across the log's full
// range.
//
// The type comes from the query rather than the path — see view.TrendPath for
// why — and an absent one is not a bad request: it is the report before a type
// has been picked, which is a page with a picker on it. That is what makes the
// route reachable by someone who does not already know what to ask it, and it
// is why this handler answers 200 where handleMonth and handleReport answer 404
// — a missing month or year is a malformed address, but a missing type is
// simply a question not yet asked.
//
// A type the log has nothing for is answered the same way, with the report
// rather than a 404: "the log says nothing about this" is an answer, and it is
// the one a retired or misspelled type has.
func handleTrend(logger *slog.Logger, log eventlog.EventStore, authn authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		typ := strings.TrimSpace(r.URL.Query().Get(view.TrendTypeParam))

		// The fold happens before a byte is written, so a log that cannot be
		// read is an honest 500 — unlike the render below, which cannot be.
		page, err := loadTrend(r.Context(), log, typ)
		if err != nil {
			logger.ErrorContext(r.Context(), "folding the log for the trend page",
				slog.String("type", typ),
				slog.Any("error", err),
			)
			http.Error(w, "the trend could not be loaded", http.StatusInternalServerError)
			return
		}

		email := sessionEmail(authn, r)

		// Logged, not answered with a 500: templ streams straight to the
		// ResponseWriter, so by the time a render can fail the status line and
		// part of the page are already on their way to the browser.
		if err := view.TypeTrend(email, page).Render(r.Context(), w); err != nil {
			logger.ErrorContext(r.Context(), "rendering the trend page",
				slog.String("type", typ),
				slog.Any("error", err),
			)
		}
	}
}

// loadTrend drains the log once and takes both halves of the trend page from
// it: the types there are to pick from, and the picked type's history.
//
// It folds only when a type was actually picked. The picker on its own needs
// KnownTypes and nothing else, and the fold is the expensive half — a full
// canonicalization plus a State over every event — so an unpicked report does
// not pay for a projection it is about to throw away.
func loadTrend(ctx context.Context, log eventlog.EventStore, typ string) (view.TrendPage, error) {
	events, err := loadEvents(ctx, log)
	if err != nil {
		return view.TrendPage{}, err
	}

	known, err := projection.KnownTypes(events)
	if err != nil {
		return view.TrendPage{}, fmt.Errorf("reading the log's types: %w", err)
	}

	page := view.TrendPage{KnownTypes: known}

	// No type picked is not a trend of nothing: the page is its picker, and
	// ProjectTrend is never asked a question with no subject — it refuses one
	// (projection.ErrInvalidType), which would turn an unpicked report into a
	// 500.
	if typ == "" {
		return page, nil
	}

	state, err := projection.Fold(events)
	if err != nil {
		return view.TrendPage{}, fmt.Errorf("folding the log: %w", err)
	}

	trend, err := projection.ProjectTrend(state, typ)
	if err != nil {
		return view.TrendPage{}, fmt.Errorf("projecting the trend for %q: %w", typ, err)
	}
	page.Trend = trend

	return page, nil
}
