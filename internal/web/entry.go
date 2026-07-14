package web

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Zaba505/expense-tracker/internal/domain"
	"github.com/Zaba505/expense-tracker/internal/eventlog"
	"github.com/Zaba505/expense-tracker/internal/money"
	"github.com/Zaba505/expense-tracker/internal/view"
)

// handleEntry records one submitted entry and answers with the month it landed
// in.
//
// One submission appends exactly one event, or none at all: a refused form is
// refused before Append is reached, so a submission the user has to correct
// leaves nothing behind in a log that could not remove it anyway. That is the
// whole reason the validation is here and not left to the store — in an
// append-only log, the moment to catch a typo is the only one there is.
//
// The reply is the panel, at 200 when the event was appended and 422 when it
// was not. Both times it is folded fresh from the log, so the values and the
// rollups the user sees are the log's, not an optimistic guess at what the log
// would say next.
func handleEntry(logger *slog.Logger, log eventlog.EventStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			// Not a form at all. There is no field to blame and no panel worth
			// rendering, so this is the one refusal that answers in plain text.
			logger.WarnContext(r.Context(), "unreadable entry submission",
				slog.Any("error", err),
			)
			http.Error(w, "that submission was not a readable form", http.StatusBadRequest)
			return
		}

		event, form := parseEntry(r.PostForm)

		if form.Rejected() {
			// Nothing is appended. The panel still folds the log as it stands,
			// so the month behind the errors is the month as it really is.
			renderPanel(w, r, logger, mustPanel(r, logger, log, panelMonth(form), form), http.StatusUnprocessableEntity)
			return
		}

		stored, err := log.Append(r.Context(), event)
		if err != nil {
			// domain.ErrInvalidEvent here is not a bad submission — parseEntry
			// checked every field Validate checks, so the log refusing one it
			// passed means the two have drifted apart. That is a bug in this
			// package, not a typo in the form, and answering 422 would tell the
			// user to fix something they did not get wrong.
			logger.ErrorContext(r.Context(), "appending an entry",
				slog.String("month", event.Month),
				slog.String("type", event.Type),
				slog.Bool("invalid_event", errors.Is(err, domain.ErrInvalidEvent)),
				slog.Any("error", err),
			)
			http.Error(w, "the entry could not be recorded", http.StatusInternalServerError)
			return
		}

		// Rendered from the event the log stored rather than the one submitted:
		// the store fills in defaults and normalizes as it writes, and what the
		// next fold will see is what came back, not what went in.
		renderPanel(w, r, logger, mustPanel(r, logger, log, stored.Month, form.Cleared()), http.StatusOK)
	}
}

// parseEntry turns a submitted form into the event to append, and returns the
// form as it should be rendered back. The event is only meaningful when the
// form was not rejected.
//
// It checks the fields one at a time instead of building an event and calling
// domain.Event.Validate, because the form has to point at the input that is
// wrong, and Validate — rightly, for a log that must refuse a bad event
// whatever wrote it — reports one error for the event as a whole. So the rules
// are the domain's (ValidMonth, Direction.Valid, Action.Valid, money.Parse) and
// only the accounting of which field broke which rule is done here. The domain
// still has the last word: Append validates the event again, and nothing in
// here can talk it into accepting one it would otherwise refuse.
func parseEntry(values url.Values) (domain.Event, view.Form) {
	form := view.Form{
		Month:      view.Field{Value: strings.TrimSpace(values.Get("month"))},
		Type:       view.Field{Value: strings.TrimSpace(values.Get("type"))},
		Amount:     view.Field{Value: strings.TrimSpace(values.Get("amount"))},
		Direction:  view.Field{Value: strings.TrimSpace(values.Get("direction"))},
		Action:     view.Field{Value: strings.TrimSpace(values.Get("action"))},
		Note:       view.Field{Value: strings.TrimSpace(values.Get("note"))},
		RefEventID: view.Field{Value: strings.TrimSpace(values.Get("refEventId"))},
	}

	// The two fields with defaults. A submission that omits them is not
	// incomplete — it is one that took the defaults, and these are the same
	// ones the empty form shows, so what an untouched form records is what an
	// untouched form says it will.
	if form.Direction.Value == "" {
		form.Direction.Value = string(domain.DirectionExpense)
	}
	if form.Action.Value == "" {
		form.Action.Value = string(domain.ActionAdd)
	}

	if !domain.ValidMonth(form.Month.Value) {
		form.Month.Error = "Pick a month."
	}
	if form.Type.Value == "" {
		form.Type.Error = "Name a type — an old one or a new one."
	}

	amount, err := money.Parse(form.Amount.Value)
	switch {
	case errors.Is(err, money.ErrRange):
		form.Amount.Error = "That amount is too large to record."
	case err != nil:
		// The message names the shapes that work, because the ones that do not
		// are rejected on purpose: "1.234" is refused rather than rounded, and
		// a user told only "invalid" would have no idea why.
		form.Amount.Error = "That is not an amount. Try 12.34, $1,234.56, or (5.00) to subtract."
	}

	// Neither of these is reachable from the form itself — a radio and a select
	// can only post what they were rendered with — so they are here for what
	// arrives without one: a curl, a stale page, a bookmarklet. The log is
	// append-only, so an entry with a direction no rollup counts would be money
	// recorded and never totalled, permanently.
	direction := domain.Direction(form.Direction.Value)
	if !direction.Valid() {
		form.Direction.Error = "Choose expense or income."
	}

	action := domain.Action(form.Action.Value)
	if !action.Valid() {
		form.Action.Error = "Choose whether to add to the month or set its total."
	}

	if form.Rejected() {
		return domain.Event{}, form
	}

	// RecordedAt is left zero for the store to stamp. The importer sets it, so
	// replayed history keeps the order it happened in; an entry made now is
	// happening now, and the log's clock is the honest one to ask.
	return domain.Event{
		Action:     action,
		Month:      form.Month.Value,
		Type:       form.Type.Value,
		Amount:     amount,
		Direction:  direction,
		Note:       form.Note.Value,
		RefEventID: form.RefEventID.Value,
	}, form
}

// panelMonth is the month to fold for a refused submission: the one submitted,
// when it is a month at all, and otherwise the current one.
//
// A month that failed to parse cannot be folded — it names no cells — but the
// panel still has to render something, and the current month is the same month
// a fresh page would have shown. The form keeps displaying what the user
// actually typed, with the error under it; only the table below it falls back.
func panelMonth(form view.Form) string {
	if form.Month.Error == "" {
		return form.Month.Value
	}
	return domain.Month(time.Now())
}

// mustPanel folds the log for the panel, and degrades to a panel with no month
// in it when the log cannot be read.
//
// The alternative is a 500, and it is the wrong answer to the request this is
// serving: by the time an entry has been appended, the event is in the log
// whether or not the fold that follows it succeeds, and a 500 would tell the
// user their entry failed when it did not. So a broken read renders the form —
// which is what they need to make the next entry — with an empty month behind
// it, and the reason goes to the log where it can be acted on.
func mustPanel(r *http.Request, logger *slog.Logger, log eventlog.EventStore, month string, form view.Form) view.Panel {
	panel, err := loadPanel(r.Context(), log, month, form)
	if err != nil {
		logger.ErrorContext(r.Context(), "folding the log for the month panel",
			slog.String("month", month),
			slog.Any("error", err),
		)
		return view.Panel{Month: month, Form: form}
	}
	return panel
}
