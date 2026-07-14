package web

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Zaba505/expense-tracker/internal/auth"
	"github.com/Zaba505/expense-tracker/internal/domain"
	"github.com/Zaba505/expense-tracker/internal/eventlog"
	"github.com/Zaba505/expense-tracker/internal/money"
	"github.com/Zaba505/expense-tracker/internal/view"
)

// testOwnerEmail is the configured allowlisted account in router tests.
const testOwnerEmail = "owner@example.com"

// stubStore stands in for the Firestore-backed event store: a real in-memory
// log, plus a readiness check the test scripts.
//
// The log half is not a mock. eventlog.Memory enforces what Firestore enforces
// — the same defaults, the same refusals, the same load order — so a handler
// test that appends an event and folds it back is exercising the append path
// the app really runs, and an entry Memory accepts here is one Firestore
// accepts in production. Only Check is stubbed, because "is the database
// reachable" is the one question an in-memory log cannot answer.
type stubStore struct {
	*eventlog.Memory

	// err is what Check answers with, and check overrides it when a test needs
	// to inspect the context the probe hands it.
	err    error
	check  func(context.Context) error
	called int
}

func newStubStore() *stubStore {
	return &stubStore{Memory: eventlog.NewMemory()}
}

func (s *stubStore) Check(ctx context.Context) error {
	s.called++
	if s.check != nil {
		return s.check(ctx)
	}
	return s.err
}

// stubAuth is a test double for the router's authenticator dependency.
type stubAuth struct {
	session            auth.Session
	hasSession         bool
	clearSessionCalled int
}

func (s *stubAuth) Session(*http.Request) (auth.Session, bool) { return s.session, s.hasSession }

func (s *stubAuth) ClearSession(w http.ResponseWriter, r *http.Request) {
	s.clearSessionCalled++
	http.SetCookie(w, &http.Cookie{
		Name:   "cleared",
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})
}

func (s *stubAuth) LoginHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://accounts.example.test", http.StatusFound)
	})
}

func (s *stubAuth) CallbackHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no login is in progress", http.StatusBadRequest)
	})
}

func (s *stubAuth) LogoutHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.ClearSession(w, r)
		http.Redirect(w, r, auth.LoginPath, http.StatusSeeOther)
	})
}

// testHandler is the real routing table, with logs thrown away and a
// dependency that is always healthy.
func testHandler() http.Handler {
	return NewHandler(slog.New(slog.DiscardHandler), newStubStore(), testOwnerEmail, testAuthenticator())
}

// testAuthenticator is a real Authenticator with a throwaway signing key
// and no issuer behind it. These tests never complete a sign-in — that is
// the auth package's own test, which stands up a fake Google — so all they
// need is the real thing mounted on the real routes.
func testAuthenticator() *auth.Authenticator {
	authn, err := auth.New(context.Background(), auth.Config{
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		SessionKey:   bytes.Repeat([]byte("k"), auth.MinSessionKeyLen),
	})
	if err != nil {
		panic(err)
	}
	return authn
}

// get runs a request against the real handler in-process, no listener.
func get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	return do(t, http.MethodGet, path)
}

func getWithHandler(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	return doWithHandler(t, h, http.MethodGet, path)
}

func do(t *testing.T, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	return doWithHandler(t, testHandler(), method, path)
}

func doWithHandler(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestLiveness(t *testing.T) {
	t.Parallel()

	rec := get(t, livenessPath)

	if rec.Code != http.StatusOK {
		t.Errorf("GET %s status = %d, want %d", livenessPath, rec.Code, http.StatusOK)
	}
	if got := rec.Body.String(); got != "ok" {
		t.Errorf("GET %s body = %q, want %q", livenessPath, got, "ok")
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Errorf("GET %s Content-Type = %q, want text/plain", livenessPath, got)
	}
}

// TestLiveness_IgnoresDependencies is the whole point of splitting the two
// probes: liveness must pass while Firestore is down, because restarting
// the container — which is what a failed liveness probe causes — cannot
// bring a database back.
func TestLiveness_IgnoresDependencies(t *testing.T) {
	t.Parallel()

	store := newStubStore()
	store.err = errors.New("firestore is down")

	rec := httptest.NewRecorder()
	NewHandler(slog.New(slog.DiscardHandler), store, testOwnerEmail, testAuthenticator()).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, livenessPath, nil))

	if rec.Code != http.StatusOK {
		t.Errorf("GET %s with a dead dependency = %d, want %d", livenessPath, rec.Code, http.StatusOK)
	}
	if store.called != 0 {
		t.Errorf("liveness consulted the event store %d times, want 0", store.called)
	}
}

// TestLiveness_RejectsNonGET pins the method to GET: the probe is a read,
// and ServeMux's "GET /health/liveness" pattern is what enforces it.
func TestLiveness_RejectsNonGET(t *testing.T) {
	t.Parallel()

	rec := do(t, http.MethodPost, livenessPath)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST %s status = %d, want %d", livenessPath, rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestHealthz(t *testing.T) {
	t.Parallel()

	rec := get(t, healthzPath)

	if rec.Code != http.StatusOK {
		t.Errorf("GET %s status = %d, want %d", healthzPath, rec.Code, http.StatusOK)
	}
	if got := rec.Body.String(); got != "ok" {
		t.Errorf("GET %s body = %q, want %q", healthzPath, got, "ok")
	}
}

func TestHome_RequiresOwnerSession(t *testing.T) {
	t.Parallel()

	rec := get(t, "/")

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("GET / status = %d, want %d", rec.Code, http.StatusSeeOther)
	}
	if got := rec.Header().Get("Location"); got != auth.LoginPath {
		t.Errorf("GET / redirected to %q, want %q", got, auth.LoginPath)
	}
}

func TestHome_OwnerSession(t *testing.T) {
	t.Parallel()

	authn := &stubAuth{
		session:    auth.Session{Email: testOwnerEmail},
		hasSession: true,
	}
	rec := getWithHandler(t, NewHandler(slog.New(slog.DiscardHandler), newStubStore(), testOwnerEmail, authn), "/")

	if rec.Code != http.StatusOK {
		t.Errorf("GET / status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("GET / Content-Type = %q, want text/html", got)
	}
	if got := rec.Body.String(); !strings.Contains(got, "<!doctype html>") {
		t.Errorf("GET / body is not an HTML document:\n%s", got)
	}
}

func TestHome_DefaultLandingMonth(t *testing.T) {
	tests := map[string]struct {
		now    string
		seed   []domain.Event
		want   string
		avoid  string
	}{
		"empty log lands on the current month": {
			now:  "2026-07",
			want: "2026-07",
		},
		"most recent activity wins when the current month is empty": {
			now: "2026-07",
			seed: []domain.Event{
				{Action: domain.ActionAdd, Month: "2026-05", Type: "Rent", Amount: money.Cents(40_00), Direction: domain.DirectionExpense},
				{Action: domain.ActionAdd, Month: "2026-06", Type: "Groceries", Amount: money.Cents(10_00), Direction: domain.DirectionExpense},
			},
			want:  "2026-06",
			avoid: "2026-07",
		},
		"current month stays the landing month when it has activity": {
			now: "2026-07",
			seed: []domain.Event{
				{Action: domain.ActionAdd, Month: "2026-06", Type: "Rent", Amount: money.Cents(40_00), Direction: domain.DirectionExpense},
				{Action: domain.ActionAdd, Month: "2026-07", Type: "Groceries", Amount: money.Cents(10_00), Direction: domain.DirectionExpense},
			},
			want:  "2026-07",
			avoid: "2026-06",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			store := newStubStore()
			for _, event := range tt.seed {
				if _, err := store.Append(t.Context(), event); err != nil {
					t.Fatalf("seeding the log with %+v: %v", event, err)
				}
			}

			authn := &stubAuth{
				session:    auth.Session{Email: testOwnerEmail},
				hasSession: true,
			}
			clockTime, err := time.Parse("2006-01", tt.now)
			if err != nil {
				t.Fatalf("parsing test month %q: %v", tt.now, err)
			}

			rec := getWithHandler(t, newHandler(slog.New(slog.DiscardHandler), store, testOwnerEmail, authn, func() time.Time { return clockTime }), "/")
			if rec.Code != http.StatusOK {
				t.Fatalf("GET / status = %d, want %d", rec.Code, http.StatusOK)
			}

			body := rec.Body.String()
			if !strings.Contains(body, "<h2>"+tt.want+"</h2>") {
				t.Fatalf("GET / body does not land on %s:\n%s", tt.want, body)
			}
			if tt.avoid != "" && strings.Contains(body, "<h2>"+tt.avoid+"</h2>") {
				t.Fatalf("GET / body landed on %s, do not want that month:\n%s", tt.avoid, body)
			}
		})
	}
}

func TestMonthView_RendersTheRequestedProjection(t *testing.T) {
	t.Parallel()

	store := newStubStore()
	for _, event := range []domain.Event{
		{Action: domain.ActionAdd, Month: "2026-06", Type: "Groceries", Amount: money.Cents(20_00), Direction: domain.DirectionExpense},
		{Action: domain.ActionAdd, Month: "2026-07", Type: "Groceries", Amount: money.Cents(10_00), Direction: domain.DirectionExpense},
		{Action: domain.ActionAdd, Month: "2026-07", Type: "Rent", Amount: money.Cents(55_00), Direction: domain.DirectionExpense},
		{Action: domain.ActionAdd, Month: "2026-07", Type: "Paycheck", Amount: money.Cents(100_00), Direction: domain.DirectionIncome},
		{Action: domain.ActionAdd, Month: "2026-08", Type: "Groceries", Amount: money.Cents(30_00), Direction: domain.DirectionExpense},
		{Action: domain.ActionAdd, Month: "2026-08", Type: "Rent", Amount: money.Cents(55_00), Direction: domain.DirectionExpense},
	} {
		if _, err := store.Append(t.Context(), event); err != nil {
			t.Fatalf("seeding the log with %+v: %v", event, err)
		}
	}

	authn := &stubAuth{
		session:    auth.Session{Email: testOwnerEmail},
		hasSession: true,
	}
	rec := getWithHandler(t, NewHandler(slog.New(slog.DiscardHandler), store, testOwnerEmail, authn), "/month/2026-07")

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /month/2026-07 status = %d, want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()
	for _, want := range []string{
		"<h2>2026-07</h2>",
		"Groceries",
		"Rent",
		"Paycheck",
		"$10.00",
		"$55.00",
		"$100.00",
		`<dd class="amount">$65.00</dd>`,
		`<dd class="amount">$35.00</dd>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /month/2026-07 body does not contain %q:\n%s", want, body)
		}
	}
}

func TestMonthView_EmptyMonthRendersAReadyForm(t *testing.T) {
	t.Parallel()

	authn := &stubAuth{
		session:    auth.Session{Email: testOwnerEmail},
		hasSession: true,
	}
	rec := getWithHandler(t, NewHandler(slog.New(slog.DiscardHandler), newStubStore(), testOwnerEmail, authn), "/month/2026-10")

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /month/2026-10 status = %d, want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()
	for _, want := range []string{
		"Nothing recorded for 2026-10 yet.",
		`name="month" value="2026-10"`,
		`value="expense" checked`,
		`value="add" selected`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /month/2026-10 body does not contain %q:\n%s", want, body)
		}
	}
}

func TestMonthView_RendersMonthNavigation(t *testing.T) {
	t.Parallel()

	authn := &stubAuth{
		session:    auth.Session{Email: testOwnerEmail},
		hasSession: true,
	}
	rec := getWithHandler(t, NewHandler(slog.New(slog.DiscardHandler), newStubStore(), testOwnerEmail, authn), "/month/2026-07")

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /month/2026-07 status = %d, want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()
	for _, want := range []string{
		`href="/month/2026-06"`,
		`hx-get="/month/2026-06"`,
		`href="/month/2026-08"`,
		`hx-get="/month/2026-08"`,
		`hx-push-url="true"`,
		`action="/month"`,
		`name="month" value="2026-07"`,
		">Go</button>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /month/2026-07 body does not contain %q:\n%s", want, body)
		}
	}
}

func TestMonthJump_HTMXReturnsJustThePanelAndCanonicalURL(t *testing.T) {
	t.Parallel()

	authn := &stubAuth{
		session:    auth.Session{Email: testOwnerEmail},
		hasSession: true,
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/month?month=2026-08", nil)
	req.Header.Set("HX-Request", "true")

	NewHandler(slog.New(slog.DiscardHandler), newStubStore(), testOwnerEmail, authn).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /month?month=2026-08 status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("HX-Push-Url"); got != "/month/2026-08" {
		t.Fatalf("GET /month?month=2026-08 HX-Push-Url = %q, want %q", got, "/month/2026-08")
	}
	body := rec.Body.String()
	if strings.Contains(body, "<!doctype html>") {
		t.Fatalf("GET /month?month=2026-08 returned a full document, want only the panel:\n%s", body)
	}
	if !strings.Contains(body, `<section id="`+view.PanelID+`"`) || !strings.Contains(body, "<h2>2026-08</h2>") {
		t.Fatalf("GET /month?month=2026-08 did not return the requested month panel:\n%s", body)
	}
}

func TestMonthView_CorrectionQueryPrefillsTheForm(t *testing.T) {
	t.Parallel()

	authn := &stubAuth{
		session:    auth.Session{Email: testOwnerEmail},
		hasSession: true,
	}
	rec := getWithHandler(t, NewHandler(slog.New(slog.DiscardHandler), newStubStore(), testOwnerEmail, authn),
		"/month/2026-10?type=Groceries&direction=income&action=set&note=fix+the+import&refEventId=evt-42")

	if rec.Code != http.StatusOK {
		t.Fatalf("GET correction-prefill status = %d, want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()
	for _, want := range []string{
		`name="type" value="Groceries"`,
		`value="income" checked`,
		`value="set" selected`,
		`name="note" value="fix the import"`,
		`name="refEventId" value="evt-42"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the prefilled month view does not contain %q:\n%s", want, body)
		}
	}
}

func TestMonthView_AuditTrailOffersCorrectionAndVoidControls(t *testing.T) {
	t.Parallel()

	store := newStubStore()
	added, err := store.Append(t.Context(), domain.Event{
		Action: domain.ActionAdd, Month: "2026-07", Type: "Groceries",
		Amount: money.Cents(10_00), Direction: domain.DirectionExpense,
	})
	if err != nil {
		t.Fatalf("seeding the add event: %v", err)
	}
	set, err := store.Append(t.Context(), domain.Event{
		Action: domain.ActionSet, Month: "2026-07", Type: "Rent",
		Amount: money.Cents(55_00), Direction: domain.DirectionExpense,
	})
	if err != nil {
		t.Fatalf("seeding the set event: %v", err)
	}

	authn := &stubAuth{
		session:    auth.Session{Email: testOwnerEmail},
		hasSession: true,
	}
	rec := getWithHandler(t, NewHandler(slog.New(slog.DiscardHandler), store, testOwnerEmail, authn), "/month/2026-07")

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /month/2026-07 status = %d, want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()
	for _, want := range []string{
		"Audit trail",
		added.ID,
		set.ID,
		"Adjust",
		"Override",
		"Void",
		`name="refEventId" value="` + added.ID + `"`,
		`name="amount" value="-10.00"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /month/2026-07 body does not contain %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, `name="refEventId" value="`+set.ID+`"`) && strings.Contains(body, `name="amount" value="-55.00"`) {
		t.Errorf("the set row rendered a one-click void, want only an override:\n%s", body)
	}
}

func TestMonthView_RejectsMalformedMonths(t *testing.T) {
	t.Parallel()

	authn := &stubAuth{
		session:    auth.Session{Email: testOwnerEmail},
		hasSession: true,
	}

	rec := getWithHandler(t, NewHandler(slog.New(slog.DiscardHandler), newStubStore(), testOwnerEmail, authn), "/month/2026-7")

	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /month/2026-7 status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestReportView_RendersTheRequestedYear(t *testing.T) {
	t.Parallel()

	store := newStubStore()
	for _, event := range []domain.Event{
		{Action: domain.ActionAdd, Month: "2025-12", Type: "Mortgage", Amount: money.Cents(999_99), Direction: domain.DirectionExpense},
		{Action: domain.ActionAdd, Month: "2026-01", Type: "Rent", Amount: money.Cents(1200_00), Direction: domain.DirectionExpense},
		{Action: domain.ActionAdd, Month: "2026-01", Type: "Travel", Amount: money.Cents(240_00), Direction: domain.DirectionExpense},
		{Action: domain.ActionAdd, Month: "2026-01", Type: "Paycheck", Amount: money.Cents(3000_00), Direction: domain.DirectionIncome},
		{Action: domain.ActionAdd, Month: "2026-02", Type: "Rent", Amount: money.Cents(1200_00), Direction: domain.DirectionExpense},
		{Action: domain.ActionAdd, Month: "2026-02", Type: "Groceries", Amount: money.Cents(120_00), Direction: domain.DirectionExpense},
		{Action: domain.ActionAdd, Month: "2026-02", Type: "Paycheck", Amount: money.Cents(3000_00), Direction: domain.DirectionIncome},
		{Action: domain.ActionAdd, Month: "2027-01", Type: "Mortgage", Amount: money.Cents(888_88), Direction: domain.DirectionExpense},
	} {
		if _, err := store.Append(t.Context(), event); err != nil {
			t.Fatalf("seeding the log with %+v: %v", event, err)
		}
	}

	authn := &stubAuth{
		session:    auth.Session{Email: testOwnerEmail},
		hasSession: true,
	}
	rec := getWithHandler(t, NewHandler(slog.New(slog.DiscardHandler), store, testOwnerEmail, authn), "/reports/2026")

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /reports/2026 status = %d, want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()
	for _, want := range []string{
		"<h1>2026</h1>",
		`href="/reports/2025"`,
		`href="/reports/2027"`,
		`href="/month/2026-01"`,
		"Groceries",
		"Paycheck",
		"Rent",
		"Travel",
		">Total<",
		">Average<",
		"$6,000.00",
		"$200.00",
		"$270.00",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /reports/2026 body does not contain %q:\n%s", want, body)
		}
	}
}

func TestReportView_RejectsMalformedYears(t *testing.T) {
	t.Parallel()

	authn := &stubAuth{
		session:    auth.Session{Email: testOwnerEmail},
		hasSession: true,
	}

	rec := getWithHandler(t, NewHandler(slog.New(slog.DiscardHandler), newStubStore(), testOwnerEmail, authn), "/reports/2026-07")

	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /reports/2026-07 status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestHome_NonOwnerSessionIsLoggedOut(t *testing.T) {
	t.Parallel()

	authn := &stubAuth{
		session:    auth.Session{Email: "other@example.com"},
		hasSession: true,
	}
	rec := getWithHandler(t, NewHandler(slog.New(slog.DiscardHandler), newStubStore(), testOwnerEmail, authn), "/")

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("GET / status = %d, want %d", rec.Code, http.StatusSeeOther)
	}
	if got := rec.Header().Get("Location"); got != auth.LoginPath {
		t.Errorf("GET / redirected to %q, want %q", got, auth.LoginPath)
	}
	if authn.clearSessionCalled != 1 {
		t.Fatalf("ClearSession called %d times, want 1", authn.clearSessionCalled)
	}
	for _, c := range rec.Result().Cookies() {
		if c.MaxAge < 0 {
			return
		}
	}
	t.Fatal("GET / with a non-owner session did not set an expiring cookie")
}

// TestAuthRoutesAreMounted pins both halves of the flow onto the routing
// table. The callback is checked with an empty query, which it must
// refuse: there is no login in progress, and a callback that answers
// anything but an error to that would be one that skipped its state check.
func TestAuthRoutesAreMounted(t *testing.T) {
	t.Parallel()

	if code := get(t, auth.LoginPath).Code; code != http.StatusFound {
		t.Errorf("GET %s = %d, want %d (a redirect to Google)", auth.LoginPath, code, http.StatusFound)
	}
	if code := get(t, auth.CallbackPath).Code; code != http.StatusBadRequest {
		t.Errorf("GET %s with no login in progress = %d, want %d",
			auth.CallbackPath, code, http.StatusBadRequest)
	}
}

func TestLogoutRouteIsMounted(t *testing.T) {
	t.Parallel()

	authn := &stubAuth{
		session:    auth.Session{Email: testOwnerEmail},
		hasSession: true,
	}
	rec := getWithHandler(t, NewHandler(slog.New(slog.DiscardHandler), newStubStore(), testOwnerEmail, authn), auth.LogoutPath)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("GET %s = %d, want %d", auth.LogoutPath, rec.Code, http.StatusSeeOther)
	}
	if got := rec.Header().Get("Location"); got != auth.LoginPath {
		t.Errorf("GET %s redirected to %q, want %q", auth.LogoutPath, got, auth.LoginPath)
	}
	if authn.clearSessionCalled != 1 {
		t.Fatalf("ClearSession called %d times, want 1", authn.clearSessionCalled)
	}
}

// TestUnknownPathIs404 guards the "/{$}" root pattern for an authorized
// caller: registered as a bare "/", the home page would match every
// unrouted path instead.
func TestUnknownPathIs404(t *testing.T) {
	t.Parallel()

	authn := &stubAuth{
		session:    auth.Session{Email: testOwnerEmail},
		hasSession: true,
	}
	rec := getWithHandler(t, NewHandler(slog.New(slog.DiscardHandler), newStubStore(), testOwnerEmail, authn), "/no-such-page")

	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /no-such-page status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestStaticAssets_RequireOwnerSession(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path string
	}{
		{path: "/static/app.css"},
		{path: "/static/htmx.min.js"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			rec := get(t, tt.path)

			if rec.Code != http.StatusSeeOther {
				t.Fatalf("GET %s status = %d, want %d", tt.path, rec.Code, http.StatusSeeOther)
			}
			if got := rec.Header().Get("Location"); got != auth.LoginPath {
				t.Errorf("GET %s redirected to %q, want %q", tt.path, got, auth.LoginPath)
			}
		})
	}
}

// assetRef finds every same-origin asset the page pulls in: the stylesheet
// href and the script src.
var assetRef = regexp.MustCompile(`(?:href|src)="(/static/[^"]+)"`)

// TestHomeAssetsAreServed is the drift test between the layout and the
// router. Renaming an asset, or moving where assets are mounted, breaks
// the page silently — the HTML still renders, it just arrives unstyled and
// without htmx. So: fetch what the page actually asks for and require a
// 200 for each.
func TestHomeAssetsAreServed(t *testing.T) {
	t.Parallel()

	authn := &stubAuth{
		session:    auth.Session{Email: testOwnerEmail},
		hasSession: true,
	}
	handler := NewHandler(slog.New(slog.DiscardHandler), newStubStore(), testOwnerEmail, authn)
	home := getWithHandler(t, handler, "/").Body.String()

	refs := assetRef.FindAllStringSubmatch(home, -1)
	if len(refs) < 2 {
		t.Fatalf("home page references %d assets, want the stylesheet and htmx:\n%s", len(refs), home)
	}

	for _, ref := range refs {
		path := ref[1]
		if code := getWithHandler(t, handler, path).Code; code != http.StatusOK {
			t.Errorf("home page references %s, but GET %s = %d, want %d", path, path, code, http.StatusOK)
		}
	}
}
