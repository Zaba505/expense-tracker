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

func TestTypeRename_PreviewsTheMoneyItWouldMove(t *testing.T) {
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
		t.Fatalf("the log holds %d events after a preview, want the 3 that were seeded", got)
	}

	body := rec.Body.String()
	for _, want := range []string{
		"Rename preview",
		"<strong>2</strong> <span>months</span>",
		"<strong>3</strong> <span>entries.</span>",
		// The impact, in money rather than in counts: January's two Fuel
		// entries come to $50.00 and land on a Gas that holds nothing yet.
		"2026-01",
		"$50.00",
		"2026-03",
		"$15.00",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the preview does not contain %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "conflict") {
		t.Errorf("the preview reports a conflict, but adds merge by summing:\n%s", body)
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
		Action: domain.ActionAdd, Month: "2026-02", Type: "Gas",
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
	if got.Amount != 0 {
		t.Errorf("the rename carries %s, want no amount", got.Amount.Display())
	}

	body := rec.Body.String()
	if !strings.Contains(body, `<td>Gas</td>`) || !strings.Contains(body, "$40.00") {
		t.Errorf("January does not show its $40.00 under Gas:\n%s", body)
	}
	if strings.Contains(body, `<td>Fuel</td>`) {
		t.Errorf("the renamed panel still shows Fuel as a row:\n%s", body)
	}
}

func TestTypeRename_RefusesAMergeThatWouldDropMoney(t *testing.T) {
	t.Parallel()

	// Both types carry a set for January — what the spreadsheet import writes.
	// Merging them can only keep the later total, so the month would fall from
	// $50.00 to $10.00. The rename is refused rather than applied.
	handler, store := ownerHandler(t)
	seed(t, store, domain.Event{
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

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("POST %s status = %d, want %d — the merge would drop $40.00\n%s",
			view.TypeRenamesPath, rec.Code, http.StatusUnprocessableEntity, rec.Body)
	}
	if got := len(logged(t, store)); got != 2 {
		t.Fatalf("the log holds %d events, want the 2 that were seeded — nothing should have been appended", got)
	}

	// The refusal is only actionable if it says which month, and shows the
	// money it is about.
	body := rec.Body.String()
	for _, want := range []string{"conflict", "2026-01", "$40.00", "$10.00"} {
		if !strings.Contains(body, want) {
			t.Errorf("the refusal does not contain %q:\n%s", want, body)
		}
	}

	// And January still totals what it always did.
	if !strings.Contains(body, "$50.00") {
		t.Errorf("January no longer totals $50.00:\n%s", body)
	}
}

func TestTypeRename_RefusesATypeTheLogHasNothingFor(t *testing.T) {
	t.Parallel()

	handler, store := ownerHandler(t)
	seed(t, store, domain.Event{
		Action: domain.ActionAdd, Month: "2026-01", Type: "Gas",
		Amount: money.Cents(10_00), Direction: domain.DirectionExpense,
	})

	rec := postTypeRename(t, handler, url.Values{
		"month":    {"2026-01"},
		"fromType": {"Diesel"},
		"toType":   {"Gas"},
		"intent":   {"apply"},
	})

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("POST %s status = %d, want %d\n%s", view.TypeRenamesPath, rec.Code, http.StatusUnprocessableEntity, rec.Body)
	}
	if got := len(logged(t, store)); got != 1 {
		t.Errorf("the log holds %d events, want the 1 that was seeded", got)
	}
}

func TestTypeRename_AnUnrecognizedIntentOnlyPreviews(t *testing.T) {
	t.Parallel()

	// A submission with no button — a curl, a stale page — must not append to a
	// log that cannot take it back. Only the apply button applies.
	handler, store := ownerHandler(t)
	seed(t, store, domain.Event{
		Action: domain.ActionAdd, Month: "2026-01", Type: "Fuel",
		Amount: money.Cents(40_00), Direction: domain.DirectionExpense,
	})

	rec := postTypeRename(t, handler, url.Values{
		"month":    {"2026-01"},
		"fromType": {"Fuel"},
		"toType":   {"Gas"},
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("POST %s status = %d, want %d\n%s", view.TypeRenamesPath, rec.Code, http.StatusOK, rec.Body)
	}
	if got := len(logged(t, store)); got != 1 {
		t.Errorf("the log holds %d events, want the 1 that was seeded — nothing should have been appended", got)
	}
}

func TestTypeRename_ShowsTheRenameInEveryMonthItReached(t *testing.T) {
	t.Parallel()

	// The rename is recorded from July but it renames January's rows. January's
	// audit trail is the one that has to explain them.
	handler, store := ownerHandler(t)
	seed(t, store, domain.Event{
		Action: domain.ActionAdd, Month: "2026-01", Type: "Fuel",
		Amount: money.Cents(40_00), Direction: domain.DirectionExpense,
	})

	rec := postTypeRename(t, handler, url.Values{
		"month":    {"2026-07"},
		"fromType": {"Fuel"},
		"toType":   {"Gas"},
		"intent":   {"apply"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST %s status = %d, want %d\n%s", view.TypeRenamesPath, rec.Code, http.StatusOK, rec.Body)
	}

	january := getWithHandler(t, handler, view.MonthPath("2026-01"))
	if january.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, want %d", view.MonthPath("2026-01"), january.Code, http.StatusOK)
	}

	body := january.Body.String()
	for _, want := range []string{"rename", "Fuel", "Gas"} {
		if !strings.Contains(body, want) {
			t.Errorf("January's audit trail does not contain %q:\n%s", want, body)
		}
	}
}
