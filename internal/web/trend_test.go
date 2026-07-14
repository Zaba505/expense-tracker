package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Zaba505/expense-tracker/internal/domain"
	"github.com/Zaba505/expense-tracker/internal/money"
	"github.com/Zaba505/expense-tracker/internal/view"
)

// trendPath is the address of one type's trend, built the way the markup builds
// it, so a test asks for a type the same way a link does — escaping included.
func trendPath(typ string) string { return view.TrendPath(typ) }

func TestTrendView_RendersATypeAcrossItsRange(t *testing.T) {
	t.Parallel()

	handler, store := ownerHandler(t)
	// Gas runs Nov to Feb and lapses in December. The log is wider than Gas is
	// — Rent reaches into March — and that is not Gas's business.
	for _, event := range []domain.Event{
		{Action: domain.ActionSet, Month: "2025-11", Type: "Gas", Amount: money.Cents(40_00), Direction: domain.DirectionExpense},
		{Action: domain.ActionSet, Month: "2025-12", Type: "Rent", Amount: money.Cents(1200_00), Direction: domain.DirectionExpense},
		{Action: domain.ActionSet, Month: "2026-01", Type: "Gas", Amount: money.Cents(60_00), Direction: domain.DirectionExpense},
		{Action: domain.ActionSet, Month: "2026-03", Type: "Rent", Amount: money.Cents(1200_00), Direction: domain.DirectionExpense},
	} {
		seed(t, store, event)
	}

	rec := getWithHandler(t, handler, trendPath("Gas"))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, want %d", trendPath("Gas"), rec.Code, http.StatusOK)
	}

	body := rec.Body.String()
	for _, want := range []string{
		"<h1>Gas</h1>",
		// Gas's own months, with the one it lapsed in between them.
		`href="/month/2025-11"`,
		`href="/month/2025-12"`,
		`href="/month/2026-01"`,
		"$40.00",
		"$60.00",
		// The lapsed month renders as an em dash, not as $0.00.
		`class="amount gap">—<`,
		// Min, max and average are over the two months Gas was recorded in.
		">Min<",
		">Max<",
		">Average<",
		"$50.00",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET %s body does not contain %q", trendPath("Gas"), want)
		}
	}

	// March is Rent's month, past the end of Gas's range: not a row here.
	if strings.Contains(body, `href="/month/2026-03"`) {
		t.Error("the trend for Gas is stretched to a month only Rent was recorded in")
	}
	// And Rent's money must not leak into Gas's rows.
	if strings.Contains(body, "$1,200.00") {
		t.Error("the trend for Gas shows Rent's amounts")
	}
}

func TestTrendView_DoesNotCountAVoidedEntryAsAMonth(t *testing.T) {
	t.Parallel()

	handler, store := ownerHandler(t)
	// The month view voids an entry by appending the opposite of it, since an
	// append-only log cannot delete one. December's cell survives at zero, and
	// the trend must read that as "this never happened" rather than as a month
	// Gas cost nothing — which would drag Min to $0.00 and halve the average.
	for _, event := range []domain.Event{
		{Action: domain.ActionAdd, Month: "2025-11", Type: "Gas", Amount: money.Cents(40_00), Direction: domain.DirectionExpense},
		{Action: domain.ActionAdd, Month: "2025-12", Type: "Gas", Amount: money.Cents(50_00), Direction: domain.DirectionExpense},
		{Action: domain.ActionAdd, Month: "2025-12", Type: "Gas", Amount: money.Cents(-50_00), Direction: domain.DirectionExpense},
		{Action: domain.ActionAdd, Month: "2026-01", Type: "Gas", Amount: money.Cents(60_00), Direction: domain.DirectionExpense},
	} {
		seed(t, store, event)
	}

	body := getWithHandler(t, handler, trendPath("Gas")).Body.String()

	// The voided month is a gap, and no $0.00 stands in for it.
	if !strings.Contains(body, `class="amount gap">—<`) {
		t.Error("the voided month does not render as a gap")
	}
	if strings.Contains(body, "$0.00") {
		t.Error("the voided month renders as $0.00: the correction became a month of history")
	}
	// Two months observed, averaging $50.00 — not three averaging $33.33.
	if !strings.Contains(body, "<strong>2</strong>") {
		t.Error("the voided month is counted among the observed months")
	}
	if !strings.Contains(body, "$50.00") {
		t.Error("the average is not over the two months Gas was actually spent in")
	}
}

func TestTrendView_PicksATypeWhenNoneIsGiven(t *testing.T) {
	t.Parallel()

	handler, store := ownerHandler(t)
	seed(t, store, domain.Event{
		Action:    domain.ActionSet,
		Month:     "2026-01",
		Type:      "Groceries",
		Amount:    money.Cents(120_00),
		Direction: domain.DirectionExpense,
	})

	rec := getWithHandler(t, handler, view.TrendsPath)

	// A report with no type picked is not a bad request — it is the page that
	// asks which type, and it offers the log's own types to pick from.
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, want %d", view.TrendsPath, rec.Code, http.StatusOK)
	}

	body := rec.Body.String()
	for _, want := range []string{
		"<h1>Type trend</h1>",
		`<option value="Groceries">`,
		`name="type"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET %s body does not contain %q", view.TrendsPath, want)
		}
	}
	if strings.Contains(body, ">Average<") {
		t.Error("a report with no type picked shows summaries anyway")
	}
}

func TestTrendView_SaysSoForATypeTheLogHasNothingFor(t *testing.T) {
	t.Parallel()

	handler, store := ownerHandler(t)
	seed(t, store, domain.Event{
		Action:    domain.ActionSet,
		Month:     "2026-01",
		Type:      "Groceries",
		Amount:    money.Cents(120_00),
		Direction: domain.DirectionExpense,
	})

	rec := getWithHandler(t, handler, trendPath("Yacht"))

	// Answered with the report, not a 404: "the log says nothing about this" is
	// an answer, and it is the one a retired or misspelled type has.
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, want %d", trendPath("Yacht"), rec.Code, http.StatusOK)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "The log has nothing recorded for") {
		t.Error("the page does not say the log has nothing for the type")
	}
	// It says nothing, rather than saying the type cost nothing.
	if strings.Contains(body, "$0.00") {
		t.Error("a type the log never mentioned is reported as having cost $0.00")
	}
}

func TestTrendView_FollowsARenamedTypeIntoItsNewName(t *testing.T) {
	t.Parallel()

	handler, store := ownerHandler(t)
	for _, event := range []domain.Event{
		{Action: domain.ActionSet, Month: "2026-01", Type: "Fuel", Amount: money.Cents(40_00), Direction: domain.DirectionExpense},
		{Action: domain.ActionRenameType, Month: "2026-02", Type: "Fuel", ToType: "Gas", Direction: domain.DirectionExpense},
		{Action: domain.ActionSet, Month: "2026-02", Type: "Gas", Amount: money.Cents(60_00), Direction: domain.DirectionExpense},
	} {
		seed(t, store, event)
	}

	// The trend of Gas reaches back through the month that was recorded as Fuel,
	// because the fold canonicalizes the rename away before the trend ever sees
	// the state.
	body := getWithHandler(t, handler, trendPath("Gas")).Body.String()
	for _, want := range []string{"$40.00", "$60.00", "$100.00"} {
		if !strings.Contains(body, want) {
			t.Errorf("the trend for Gas does not contain %q", want)
		}
	}
}

func TestTrendView_AddressesATypeWhoseNameIsNotPathSafe(t *testing.T) {
	t.Parallel()

	// A type is free-form text, and the things an owner types are not all
	// path-safe. This is why the type is a query parameter: as a path segment,
	// the slash in this name would be a route boundary and the report would be
	// of a type nobody has.
	const typ = "Dining / Takeout"

	handler, store := ownerHandler(t)
	seed(t, store, domain.Event{
		Action:    domain.ActionSet,
		Month:     "2026-01",
		Type:      typ,
		Amount:    money.Cents(85_00),
		Direction: domain.DirectionExpense,
	})

	rec := getWithHandler(t, handler, trendPath(typ))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, want %d", trendPath(typ), rec.Code, http.StatusOK)
	}
	if body := rec.Body.String(); !strings.Contains(body, "$85.00") {
		t.Errorf("the trend for %q does not contain its amount", typ)
	}
}

func TestTrendPath_EscapesTheType(t *testing.T) {
	t.Parallel()

	got := view.TrendPath("Gas & Electric")
	want := view.TrendsPath + "?type=" + url.QueryEscape("Gas & Electric")
	if got != want {
		t.Fatalf("TrendPath() = %q, want %q", got, want)
	}
}

// TestTrendRoute_DoesNotShadowTheYearlyReport pins the mux precedence the trend
// route depends on: "/reports/types" is the more specific pattern, so it wins
// over "/reports/{year}" — and it must not have cost the year report its own
// route in the process.
func TestTrendRoute_DoesNotShadowTheYearlyReport(t *testing.T) {
	t.Parallel()

	handler, store := ownerHandler(t)
	seed(t, store, domain.Event{
		Action:    domain.ActionSet,
		Month:     "2026-01",
		Type:      "Rent",
		Amount:    money.Cents(1200_00),
		Direction: domain.DirectionExpense,
	})

	year := getWithHandler(t, handler, "/reports/2026")
	if year.Code != http.StatusOK {
		t.Fatalf("GET /reports/2026 status = %d, want %d", year.Code, http.StatusOK)
	}
	if body := year.Body.String(); !strings.Contains(body, "<h1>2026</h1>") {
		t.Error("GET /reports/2026 is no longer the yearly report")
	}

	// And the year grid's type columns are the way into the trend.
	if body := year.Body.String(); !strings.Contains(body, `href="`+view.TrendPath("Rent")+`"`) {
		t.Error("the yearly report does not link its type columns to their trends")
	}

	trend := getWithHandler(t, handler, view.TrendsPath)
	if trend.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, want %d", view.TrendsPath, trend.Code, http.StatusOK)
	}
	if body := trend.Body.String(); !strings.Contains(body, "<h1>Type trend</h1>") {
		t.Error("GET /reports/types fell through to the yearly report")
	}
}
