package web

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/Zaba505/expense-tracker/internal/eventlog"
	"github.com/Zaba505/expense-tracker/internal/projection"
	"github.com/Zaba505/expense-tracker/internal/view"
)

const yearLayout = "2006"

// handleReport renders one requested year's overview.
func handleReport(logger *slog.Logger, log eventlog.EventStore, authn authenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		year := r.PathValue("year")
		if !validYear(year) {
			http.NotFound(w, r)
			return
		}

		renderYearPage(w, r, logger, log, authn, year)
	}
}

func renderYearPage(w http.ResponseWriter, r *http.Request, logger *slog.Logger, log eventlog.EventStore, authn authenticator, year string) {
	var email string
	if session, ok := authn.Session(r); ok {
		email = session.Email
	}

	report, err := loadYear(r.Context(), log, year)
	if err != nil {
		logger.ErrorContext(r.Context(), "folding the log for the year page",
			slog.String("year", year),
			slog.Any("error", err),
		)
		http.Error(w, "the yearly report could not be loaded", http.StatusInternalServerError)
		return
	}

	if err := view.YearReport(email, report, previousYear(year), nextYear(year)).Render(r.Context(), w); err != nil {
		logger.ErrorContext(r.Context(), "rendering the year page",
			slog.String("year", year),
			slog.Any("error", err),
		)
	}
}

func loadYear(ctx context.Context, log eventlog.EventStore, year string) (projection.Year, error) {
	_, state, err := loadState(ctx, log)
	if err != nil {
		return projection.Year{}, err
	}

	report, err := projection.ProjectYear(state, year)
	if err != nil {
		return projection.Year{}, err
	}
	return report, nil
}

func validYear(year string) bool {
	_, err := time.Parse(yearLayout, year)
	return err == nil
}

func previousYear(year string) string {
	y := mustParseYear(year)
	return strconv.Itoa(y - 1)
}

func nextYear(year string) string {
	y := mustParseYear(year)
	return strconv.Itoa(y + 1)
}

func mustParseYear(year string) int {
	y, err := strconv.Atoi(year)
	if err != nil {
		panic("web: invalid year " + strconv.Quote(year))
	}
	return y
}
