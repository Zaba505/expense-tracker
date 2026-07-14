package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Zaba505/expense-tracker/internal/domain"
	"github.com/Zaba505/expense-tracker/internal/money"
	"github.com/Zaba505/expense-tracker/internal/view"
)

func postTypeRename(t *testing.T, h http.Handler, form url.Values) *httptest.ResponseRecorder {
	t.Helper()

	r := httptest.NewRequest(http.MethodPost, view.TypeRenamesPath, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestTypeRename_PreviewsAffectedMonthsAndCounts(t *testing.T) {
	t.Parallel()

	handler, store := ownerHandler(t)
	seed(t, store, domain.Event{
		Action: domain.ActionAdd, Month: "2026-01", Type: "Fuel",
		Amount: money.Cents(40_00), Direction: domain.DirectionExpense,
	})
	seed(t, store, domain.Event{
		Action: domain.ActionAdd, Month: "2026-01", Type: "Fuel",
		Amount: money.Cents(10_00), Direction: domain.DirectionExpense,
	})
	seed(t, store, domain.Event{
		Action: domain.ActionAdd, Month: "2026-03", Type: "Fuel",
		Amount: money.Cents(15_00), Direction: domain.DirectionExpense,
	})

	rec := postTypeRename(t, handler, url.Values{
		"month":    {testMonth},
		"fromType": {"Fuel"},
		"toType":   {"Gas"},
		"intent":   {"preview"},
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("POST %s status = %d, want %d\n%s", view.TypeRenamesPath, rec.Code, http.StatusOK, rec.Body)
	}
	if got := len(logged(t, store)); got != 3 {
		t.Fatalf("preview appended %d events, want none", got-3)
	}

	body := rec.Body.String()
	for _, want := range []string{
		"Rename preview",
		"Fuel",
		"Gas",
		"2026-01",
		"2026-03",
		"2 entries",
		"1 entry",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("preview panel does not contain %q:\n%s", want, body)
		}
	}
}

func TestTypeRename_AppendsAnEventAndReprojectsHistory(t *testing.T) {
	t.Parallel()

	handler, store := ownerHandler(t)
	original := seed(t, store, domain.Event{
		Action: domain.ActionSet, Month: "2026-01", Type: "Fuel",
		Amount: money.Cents(40_00), Direction: domain.DirectionExpense,
	})
	seed(t, store, domain.Event{
		Action: domain.ActionSet, Month: "2026-01", Type: "Gas",
		Amount: money.Cents(10_00), Direction: domain.DirectionExpense,
	})

	rec := postTypeRename(t, handler, url.Values{
		"month":    {"2026-01"},
		"fromType": {"Fuel"},
		"toType":   {"Gas"},
		"intent":   {"apply"},
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("POST %s status = %d, want %d\n%s", view.TypeRenamesPath, rec.Code, http.StatusOK, rec.Body)
	}

	events := logged(t, store)
	if len(events) != 3 {
		t.Fatalf("the log holds %d events, want 3", len(events))
	}
	if got := events[0]; got.ID != original.ID || got.Type != original.Type || got.Amount != original.Amount {
		t.Fatalf("the original event changed from %+v to %+v", original, got)
	}

	got := events[2]
	if got.Action != domain.ActionRenameType || got.Type != "Fuel" || got.ToType != "Gas" {
		t.Errorf("the appended event is %+v, want a Fuel -> Gas type rename", got)
	}

	body := rec.Body.String()
	for _, want := range []string{
		`<td>Gas</td>`,
		"$10.00",
		"rename",
		"to",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the renamed panel does not contain %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, `<td>Fuel</td>`) {
		t.Errorf("the renamed panel still shows Fuel as a row:\n%s", body)
	}
}
