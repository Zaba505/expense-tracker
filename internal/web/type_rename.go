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
	"github.com/Zaba505/expense-tracker/internal/projection"
	"github.com/Zaba505/expense-tracker/internal/view"
)

// handleTypeRename previews a retroactive rename or merge, and records it.
//
// Both intents run the same preview, because the preview is not a courtesy —
// it is the check. A merge can destroy money (see
// projection.TypeRenameCell.Conflict), so the preview is what decides whether
// there is a rename to append at all, and "apply" is refused on the same
// grounds "preview" would have warned about. A user who never pressed Preview
// is therefore no more able to lose a month's total than one who did.
//
// The reply is the panel either way, at 200 when the rename was appended or
// merely previewed, and 422 when it was refused.
func handleTypeRename(logger *slog.Logger, log eventlog.EventStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			logger.WarnContext(r.Context(), "unreadable type-rename submission",
				slog.Any("error", err),
			)
			http.Error(w, "that submission was not a readable form", http.StatusBadRequest)
			return
		}

		month := typeRenameMonth(r.PostForm)
		form, intent := parseTypeRename(r.PostForm)
		if form.Rejected() {
			panel := mustPanel(r, logger, log, month, view.NewForm(month))
			panel.TypeRenameForm = form
			renderPanel(w, r, logger, panel, http.StatusUnprocessableEntity)
			return
		}

		events, state, err := loadState(r.Context(), log)
		if err != nil {
			logger.ErrorContext(r.Context(), "loading the log for a type rename",
				slog.String("month", month),
				slog.Any("error", err),
			)
			http.Error(w, "the expenses could not be loaded", http.StatusInternalServerError)
			return
		}

		preview, err := projection.PreviewTypeRename(events, form.FromType.Value, form.ToType.Value)
		if err != nil {
			logger.ErrorContext(r.Context(), "previewing a type rename",
				slog.String("month", month),
				slog.String("from_type", form.FromType.Value),
				slog.String("to_type", form.ToType.Value),
				slog.Any("error", err),
			)
			http.Error(w, "the rename could not be previewed", http.StatusInternalServerError)
			return
		}

		// What the preview found, said as the form's own refusals. Empty fields
		// are already caught above, so what is left is what only the log knows:
		// a type it has nothing to rename, and a merge it cannot make without
		// losing money.
		switch {
		case preview.FromType == preview.ToType:
			form.ToType.Error = "Pick a different target type."
		case preview.AffectedEntries == 0:
			form.FromType.Error = "The log has nothing recorded under that type."
		case preview.Conflicts:
			form.FromType.Error = "Both types already have a total set in the same month, so merging them would drop one. Set the combined total first, then rename."
		}

		if form.Rejected() || intent != intentApply {
			status := http.StatusOK
			if form.Rejected() {
				status = http.StatusUnprocessableEntity
			}

			panel, err := panelFromState(events, state, month, view.NewForm(month))
			if err != nil {
				logger.ErrorContext(r.Context(), "folding the log for the type-rename panel",
					slog.String("month", month),
					slog.Any("error", err),
				)
				http.Error(w, "the expenses could not be loaded", http.StatusInternalServerError)
				return
			}
			panel.TypeRenameForm = form

			// The preview is rendered with the refusal rather than instead of
			// it: a conflict is only actionable if the owner can see the months
			// it is about.
			panel.TypeRenamePreview = preview
			renderPanel(w, r, logger, panel, status)
			return
		}

		stored, err := log.Append(r.Context(), domain.Event{
			Action: domain.ActionRenameType,
			Month:  month,
			Type:   preview.FromType,
			ToType: preview.ToType,
		})
		if err != nil {
			logger.ErrorContext(r.Context(), "appending a type rename",
				slog.String("month", month),
				slog.String("from_type", preview.FromType),
				slog.String("to_type", preview.ToType),
				slog.Bool("invalid_event", errors.Is(err, domain.ErrInvalidEvent)),
				slog.Any("error", err),
			)
			http.Error(w, "the rename could not be recorded", http.StatusInternalServerError)
			return
		}

		panel := mustPanel(r, logger, log, stored.Month, view.NewForm(stored.Month))
		renderPanel(w, r, logger, panel, http.StatusOK)
	}
}

// intentApply is the submit button that records the rename. Anything else —
// the Preview button, or a submission with no button at all — only previews,
// because the safe reading of an unrecognized intent is the one that appends
// nothing to a log that cannot take it back.
const intentApply = "apply"

func parseTypeRename(values url.Values) (view.TypeRenameForm, string) {
	form := view.TypeRenameForm{
		FromType: view.Field{Value: strings.TrimSpace(values.Get("fromType"))},
		ToType:   view.Field{Value: strings.TrimSpace(values.Get("toType"))},
	}

	if form.FromType.Value == "" {
		form.FromType.Error = "Choose the type to rename."
	}
	if form.ToType.Value == "" {
		form.ToType.Error = "Name the type it should become."
	}

	return form, strings.TrimSpace(values.Get("intent"))
}

// typeRenameMonth is the month to fold behind the form, on the same terms as
// panelMonth: the one submitted when it is a month at all, and otherwise the
// current one. It is only the month the panel renders — a rename reaches every
// month regardless of which one it was recorded from.
func typeRenameMonth(values url.Values) string {
	month := strings.TrimSpace(values.Get("month"))
	if domain.ValidMonth(month) {
		return month
	}
	return domain.Month(time.Now())
}
