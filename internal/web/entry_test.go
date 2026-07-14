package web

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Zaba505/expense-tracker/internal/auth"
	"github.com/Zaba505/expense-tracker/internal/domain"
	"github.com/Zaba505/expense-tracker/internal/money"
	"github.com/Zaba505/expense-tracker/internal/view"
)

// testMonth is the month the entry tests submit for. It is a fixed month
// rather than the current one, because an entry can be recorded against any
// month — that is the point of the field — and a test that used "now" would be
// testing the clock as well as the handler.
const testMonth = "2026-07"

// ownerHandler is the real routing table with the owner already signed in, and
// the log it serves. The log is a real eventlog.Memory, so what these tests
// read back is what the handler actually appended, not what a mock recorded.
func ownerHandler(t *testing.T) (http.Handler, *stubStore) {
	t.Helper()

	store := newStubStore()
	authn := &stubAuth{
		session:    auth.Session{Email: testOwnerEmail},
		hasSession: true,
	}
	return NewHandler(slog.New(slog.DiscardHandler), store, testOwnerEmail, authn), store
}

// entry is a submission with everything filled in. Tests vary one field, so a
// failure names the field that broke it.
func entry() url.Values {
	return url.Values{
		"month":     {testMonth},
		"type":      {"Groceries"},
		"amount":    {"12.34"},
		"direction": {string(domain.DirectionExpense)},
		"action":    {string(domain.ActionAdd)},
	}
}

// postEntry submits the form the way the browser does.
func postEntry(t *testing.T, h http.Handler, form url.Values) *httptest.ResponseRecorder {
	t.Helper()

	r := httptest.NewRequest(http.MethodPost, view.EntriesPath, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// seed appends an event straight to the log, standing in for entries made
// before the one under test.
func seed(t *testing.T, store *stubStore, e domain.Event) {
	t.Helper()

	if _, err := store.Append(context.Background(), e); err != nil {
		t.Fatalf("seeding the log with %+v: %v", e, err)
	}
}

// logged is everything the log holds, in the log's order.
func logged(t *testing.T, store *stubStore) []domain.Event {
	t.Helper()

	var events []domain.Event
	for event, err := range store.Load(context.Background()) {
		if err != nil {
			t.Fatalf("loading the log: %v", err)
		}
		events = append(events, event)
	}
	return events
}

// TestEntry_AppendsExactlyOneEvent is the story's first promise. One
// submission is one fact: not two, and — since the log cannot take an event
// back — not one plus a retry.
func TestEntry_AppendsExactlyOneEvent(t *testing.T) {
	t.Parallel()

	handler, store := ownerHandler(t)

	form := entry()
	form.Set("amount", "$1,234.56")
	rec := postEntry(t, handler, form)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST %s status = %d, want %d\n%s", view.EntriesPath, rec.Code, http.StatusOK, rec.Body)
	}

	events := logged(t, store)
	if len(events) != 1 {
		t.Fatalf("the log holds %d events, want exactly 1", len(events))
	}

	got := events[0]
	want := domain.Event{
		Action:    domain.ActionAdd,
		Month:     testMonth,
		Type:      "Groceries",
		Amount:    money.Cents(123_456),
		Direction: domain.DirectionExpense,
	}
	if got.Action != want.Action || got.Month != want.Month || got.Type != want.Type ||
		got.Amount != want.Amount || got.Direction != want.Direction {
		t.Errorf("appended %+v, want %+v", got, want)
	}

	// The store's to assign, and the fold's to order by. An event carrying
	// neither is an event the log cannot place.
	if got.ID == "" {
		t.Error("the appended event has no ID; the store assigns one on append")
	}
	if got.RecordedAt.IsZero() {
		t.Error("the appended event has no RecordedAt; the log orders by it")
	}
}

// TestEntry_AnswersWithTheFoldedMonth is the story's second promise: the reply
// is the month panel, folded fresh, so the page cannot end up showing a total
// that predates the entry that just moved it.
func TestEntry_AnswersWithTheFoldedMonth(t *testing.T) {
	t.Parallel()

	handler, store := ownerHandler(t)
	seed(t, store, domain.Event{
		Action: domain.ActionAdd, Month: testMonth, Type: "Groceries",
		Amount: money.Cents(10_00), Direction: domain.DirectionExpense,
	})

	form := entry()
	form.Set("amount", "5.00")
	rec := postEntry(t, handler, form)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST %s status = %d, want %d", view.EntriesPath, rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", got)
	}

	body := rec.Body.String()

	// A fragment, not a page: htmx swaps this into the panel it replaces, and
	// a whole document swapped inside <main> would nest a second <html>.
	if strings.Contains(body, "<!doctype html>") {
		t.Errorf("the reply is a whole document, want just the panel:\n%s", body)
	}
	if !strings.Contains(body, `id="`+view.PanelID+`"`) {
		t.Errorf("the reply is not the month panel htmx asked for (no %s):\n%s", view.PanelID, body)
	}

	// $10 was already there and $5 was just added, so the cell — and the
	// month's expenses — read $15.00. The add folded; it did not replace.
	if !strings.Contains(body, "$15.00") {
		t.Errorf("the panel does not show the folded $15.00:\n%s", body)
	}
	if strings.Contains(body, "$5.00") {
		t.Errorf("the panel shows the raw $5.00 that was submitted, not the folded total:\n%s", body)
	}
}

// TestEntry_ActionsFold covers the two actions doing what the log says they
// do, through the handler rather than the projection: an add sums with what is
// there, and a set supersedes it.
func TestEntry_ActionsFold(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		action domain.Action
		amount string
		want   string
	}{
		"add sums with the running amount": {
			action: domain.ActionAdd,
			amount: "5.00",
			want:   "$15.00",
		},
		"set supersedes it": {
			action: domain.ActionSet,
			amount: "3.00",
			want:   "$3.00",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			handler, store := ownerHandler(t)
			seed(t, store, domain.Event{
				Action: domain.ActionAdd, Month: testMonth, Type: "Groceries",
				Amount: money.Cents(10_00), Direction: domain.DirectionExpense,
			})

			form := entry()
			form.Set("action", string(test.action))
			form.Set("amount", test.amount)
			rec := postEntry(t, handler, form)

			if rec.Code != http.StatusOK {
				t.Fatalf("POST %s status = %d, want %d", view.EntriesPath, rec.Code, http.StatusOK)
			}
			if body := rec.Body.String(); !strings.Contains(body, test.want) {
				t.Errorf("after a %q of %s over $10.00, the panel does not show %s:\n%s", test.action, test.amount, test.want, body)
			}
		})
	}
}

// TestEntry_IncomeIsCountedAsIncome walks the direction toggle through to the
// rollup: income is money in, so it lands on the other side of the net.
func TestEntry_IncomeIsCountedAsIncome(t *testing.T) {
	t.Parallel()

	handler, store := ownerHandler(t)
	seed(t, store, domain.Event{
		Action: domain.ActionAdd, Month: testMonth, Type: "Groceries",
		Amount: money.Cents(200_00), Direction: domain.DirectionExpense,
	})

	form := entry()
	form.Set("type", "Paycheck")
	form.Set("amount", "5,000.00")
	form.Set("direction", string(domain.DirectionIncome))
	rec := postEntry(t, handler, form)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST %s status = %d, want %d", view.EntriesPath, rec.Code, http.StatusOK)
	}

	events := logged(t, store)
	if got := events[len(events)-1].Direction; got != domain.DirectionIncome {
		t.Errorf("the appended event's direction is %q, want %q", got, domain.DirectionIncome)
	}

	// $5,000 in, $200 out, so $4,800 was left. A rollup that had counted the
	// paycheck as an expense would read -$5,200.00 here.
	if body := rec.Body.String(); !strings.Contains(body, "$4,800.00") {
		t.Errorf("the rollup does not net the income against the expense ($4,800.00):\n%s", body)
	}
}

// TestEntry_Defaults is what an untouched form records. The two fields with
// defaults are defaulted to the domain's own — an expense, added — so a
// submission that says nothing about either records what the form claims it
// will.
func TestEntry_Defaults(t *testing.T) {
	t.Parallel()

	handler, store := ownerHandler(t)

	form := entry()
	form.Del("direction")
	form.Del("action")
	rec := postEntry(t, handler, form)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST %s status = %d, want %d\n%s", view.EntriesPath, rec.Code, http.StatusOK, rec.Body)
	}

	events := logged(t, store)
	if len(events) != 1 {
		t.Fatalf("the log holds %d events, want exactly 1", len(events))
	}
	if got := events[0].Direction; got != domain.DirectionExpense {
		t.Errorf("a submission with no direction recorded %q, want %q", got, domain.DirectionExpense)
	}
	if got := events[0].Action; got != domain.ActionAdd {
		t.Errorf("a submission with no action recorded %q, want %q", got, domain.ActionAdd)
	}
}

// TestEntry_Refuses is the story's fourth promise, and the one an append-only
// log leans on hardest: a bad submission is caught before it is written,
// because afterwards there is nothing to fix. Every case appends nothing.
func TestEntry_Refuses(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		mutate func(url.Values)
		field  string // the input the message must land under
	}{
		"an amount that is not one": {
			mutate: func(f url.Values) { f.Set("amount", "twelve dollars") },
			field:  "amount",
		},
		"an amount finer than a cent, which is refused rather than rounded": {
			mutate: func(f url.Values) { f.Set("amount", "1.234") },
			field:  "amount",
		},
		"an empty amount": {
			mutate: func(f url.Values) { f.Set("amount", "") },
			field:  "amount",
		},
		"an amount too large to hold in cents": {
			mutate: func(f url.Values) { f.Set("amount", "99999999999999999999") },
			field:  "amount",
		},
		"a month that does not exist": {
			mutate: func(f url.Values) { f.Set("month", "2026-13") },
			field:  "month",
		},
		"a month without its leading zero, which would sort after December": {
			mutate: func(f url.Values) { f.Set("month", "2026-7") },
			field:  "month",
		},
		"a day, which is not a month": {
			mutate: func(f url.Values) { f.Set("month", "2026-07-12") },
			field:  "month",
		},
		"no month at all": {
			mutate: func(f url.Values) { f.Del("month") },
			field:  "month",
		},
		"an empty type": {
			mutate: func(f url.Values) { f.Set("type", "") },
			field:  "type",
		},
		"a type that is only whitespace": {
			mutate: func(f url.Values) { f.Set("type", "   ") },
			field:  "type",
		},
		// Neither of these can come from the form — a radio and a select post
		// only what they were rendered with — but a curl can send them, and an
		// event no rollup counts is money recorded and never totalled.
		"a direction the rollups cannot count": {
			mutate: func(f url.Values) { f.Set("direction", "refund") },
			field:  "direction",
		},
		"an action the fold has no case for": {
			mutate: func(f url.Values) { f.Set("action", "subtract") },
			field:  "action",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			handler, store := ownerHandler(t)

			form := entry()
			test.mutate(form)
			rec := postEntry(t, handler, form)

			// 422, not 200: the status line is the only part of the answer a
			// cache, a log, or a test reads, and a 200 would say the entry was
			// recorded when nothing was. htmx is configured to swap it anyway,
			// so the reason still reaches the page.
			if rec.Code != http.StatusUnprocessableEntity {
				t.Errorf("POST %s status = %d, want %d", view.EntriesPath, rec.Code, http.StatusUnprocessableEntity)
			}

			if events := logged(t, store); len(events) != 0 {
				t.Fatalf("a refused submission appended %d events: %+v", len(events), events)
			}

			// The message is rendered, and it is rendered under the field at
			// fault rather than as one anonymous complaint about the form.
			body := rec.Body.String()
			if !strings.Contains(body, `class="field-error"`) {
				t.Errorf("the refused form carries no message:\n%s", body)
			}
			if !strings.Contains(body, `aria-invalid="true"`) && test.field != "direction" && test.field != "action" {
				t.Errorf("the %s input is not marked invalid:\n%s", test.field, body)
			}
		})
	}
}

// TestEntry_RefusalKeepsWhatWasTyped: a rejected submission comes back with
// the values still in it. Retyping the month, the type, and the amount to fix
// one of them is exactly the busywork this form exists to remove.
func TestEntry_RefusalKeepsWhatWasTyped(t *testing.T) {
	t.Parallel()

	handler, _ := ownerHandler(t)

	form := entry()
	form.Set("type", "Mortgage")
	form.Set("amount", "12.3.4")
	form.Set("direction", string(domain.DirectionIncome))
	rec := postEntry(t, handler, form)

	body := rec.Body.String()
	for _, want := range []string{
		`value="` + testMonth + `"`, // the month is still selected
		`value="Mortgage"`,          // the type is still typed
		`value="12.3.4"`,            // the amount that failed is still there to fix
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the refused form dropped %s:\n%s", want, body)
		}
	}

	// And the direction toggle still shows the side they picked, rather than
	// snapping back to the default and quietly recording an expense next time.
	income := `value="` + string(domain.DirectionIncome) + `"` + " checked"
	if !strings.Contains(body, income) {
		t.Errorf("the refused form lost the income toggle:\n%s", body)
	}
}

// TestEntry_RefusalShowsTheMonthItRefusedFor: behind the errors, the panel
// still shows the month as the log really has it. The submission was refused,
// so nothing about the month changed, and the totals must not pretend it did.
func TestEntry_RefusalShowsTheMonthAsItIs(t *testing.T) {
	t.Parallel()

	handler, store := ownerHandler(t)
	seed(t, store, domain.Event{
		Action: domain.ActionAdd, Month: testMonth, Type: "Groceries",
		Amount: money.Cents(10_00), Direction: domain.DirectionExpense,
	})

	form := entry()
	form.Set("amount", "not an amount")
	rec := postEntry(t, handler, form)

	if body := rec.Body.String(); !strings.Contains(body, "$10.00") {
		t.Errorf("the refused submission's panel does not show the month as it stands ($10.00):\n%s", body)
	}
}

// TestEntry_SuccessClearsTheFieldsThatChange: the type and the amount come
// back empty, ready for the next entry, and the month, direction, and action
// stay. A month's bills are entered in one sitting.
func TestEntry_SuccessClearsTheFieldsThatChange(t *testing.T) {
	t.Parallel()

	handler, _ := ownerHandler(t)

	form := entry()
	form.Set("type", "Mortgage")
	form.Set("amount", "1500.00")
	form.Set("action", string(domain.ActionSet))
	rec := postEntry(t, handler, form)

	body := rec.Body.String()

	// The type input is empty — the "Mortgage" still on the page is the row it
	// just folded into, and the datalist option, not the value of the input.
	if strings.Contains(body, `name="type" value="Mortgage"`) {
		t.Errorf("the type is still in the form after being recorded:\n%s", body)
	}
	if strings.Contains(body, `value="1500.00"`) {
		t.Errorf("the amount is still in the form after being recorded:\n%s", body)
	}
	if !strings.Contains(body, `value="`+testMonth+`"`) {
		t.Errorf("the month was cleared; entries come in runs, so it should stay:\n%s", body)
	}
	if !strings.Contains(body, `value="`+string(domain.ActionSet)+`" selected`) {
		t.Errorf("the action was reset; the next entry in a run is usually the same kind:\n%s", body)
	}
}

// TestEntry_OffersTheLogsTypesForAutocomplete: the type field completes from
// what the log has actually seen, and a type invented in one submission is
// offered in the next. Nothing declares a category anywhere.
func TestEntry_OffersTheLogsTypesForAutocomplete(t *testing.T) {
	t.Parallel()

	handler, store := ownerHandler(t)
	seed(t, store, domain.Event{
		Action: domain.ActionAdd, Month: "2026-01", Type: "Insurance",
		Amount: money.Cents(80_00), Direction: domain.DirectionExpense,
	})
	seed(t, store, domain.Event{
		Action: domain.ActionAdd, Month: "2026-06", Type: "Utilities",
		Amount: money.Cents(12_00), Direction: domain.DirectionExpense,
	})

	form := entry()
	form.Set("type", "Kayak Repairs") // a type made up on the spot
	rec := postEntry(t, handler, form)

	body := rec.Body.String()
	if !strings.Contains(body, `name="type"`) || !strings.Contains(body, `list="known-types"`) {
		t.Errorf("the type input is not wired to the known-types datalist:\n%s", body)
	}
	if !strings.Contains(body, `<option value="Kayak Repairs">`) {
		t.Errorf("the type just recorded is not offered for autocomplete:\n%s", body)
	}
	// Last used in January and never since, but still worth suggesting in July:
	// known types span the log, not the month on screen.
	if !strings.Contains(body, `<option value="Insurance">`) {
		t.Errorf("a type from another month is not offered for autocomplete:\n%s", body)
	}
	if !strings.Contains(body, `<option value="Utilities">`) {
		t.Errorf("a recently used type is not offered for autocomplete:\n%s", body)
	}

	kayak := strings.Index(body, `<option value="Kayak Repairs">`)
	utilities := strings.Index(body, `<option value="Utilities">`)
	insurance := strings.Index(body, `<option value="Insurance">`)
	if !(kayak < utilities && utilities < insurance) {
		t.Errorf("autocomplete options are not newest-first in the response:\n%s", body)
	}
}

// TestEntry_RequiresOwnerSession: the write path is behind the same gate the
// page is. A stranger who finds the route posts nothing into the log.
func TestEntry_RequiresOwnerSession(t *testing.T) {
	t.Parallel()

	store := newStubStore()
	handler := NewHandler(slog.New(slog.DiscardHandler), store, testOwnerEmail, &stubAuth{})

	rec := postEntry(t, handler, entry())

	if rec.Code != http.StatusSeeOther {
		t.Errorf("POST %s with no session = %d, want %d", view.EntriesPath, rec.Code, http.StatusSeeOther)
	}
	if events := logged(t, store); len(events) != 0 {
		t.Fatalf("an unauthenticated submission appended %d events: %+v", len(events), events)
	}
}

// TestEntry_RejectsGET pins the method: an entry is a write, and a log that
// could be appended to by following a link is one a prefetch could write to.
func TestEntry_RejectsGET(t *testing.T) {
	t.Parallel()

	handler, store := ownerHandler(t)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, view.EntriesPath, nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET %s = %d, want %d", view.EntriesPath, rec.Code, http.StatusMethodNotAllowed)
	}
	if events := logged(t, store); len(events) != 0 {
		t.Fatalf("a GET appended %d events: %+v", len(events), events)
	}
}
