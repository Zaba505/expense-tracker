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

		switch {
		case preview.FromType == "":
			form.FromType.Error = "Choose the type to rename."
		case preview.ToType == "":
			form.ToType.Error = "Name the type it should become."
		case preview.FromType == preview.ToType:
			form.ToType.Error = "Pick a different target type."
		case preview.AffectedEntries == 0:
			form.FromType.Error = "That type has no history left to rename."
		}

		if form.Rejected() || intent == "preview" || intent == "" {
			status := http.StatusOK
			if form.Rejected() {
				status = http.StatusUnprocessableEntity
				preview = projection.TypeRenamePreview{}
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
			panel.TypeRenamePreview = preview
			renderPanel(w, r, logger, panel, status)
			return
		}

		stored, err := log.Append(r.Context(), domain.Event{
			Action:    domain.ActionRenameType,
			Month:     month,
			Type:      preview.FromType,
			ToType:    preview.ToType,
			Direction: domain.DirectionExpense,
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

func typeRenameMonth(values url.Values) string {
	month := strings.TrimSpace(values.Get("month"))
	if domain.ValidMonth(month) {
		return month
	}
	return domain.Month(time.Now())
}
