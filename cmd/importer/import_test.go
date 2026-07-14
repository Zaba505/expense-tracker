package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/Zaba505/expense-tracker/internal/domain"
	"github.com/Zaba505/expense-tracker/internal/eventlog"
	"github.com/Zaba505/expense-tracker/internal/money"
	"github.com/Zaba505/expense-tracker/internal/projection"
)

// These run against eventlog.Memory, which is held to the same contract as the
// Firestore store by the conformance suite the two share — the same defaults,
// the same refusals, the same idempotence. So what an import does here is what
// it does to a database, which is the only reason a test of a thing that cannot
// be undone is worth writing. The emulator half is import_integration_test.go.

// sampleEvents is the committed sample, parsed: the file every test below
// imports.
func sampleEvents(t *testing.T) []domain.Event {
	t.Helper()

	events, err := parseFile(samplePath, importedAt)
	if err != nil {
		t.Fatalf("parseFile(%s): %v", samplePath, err)
	}
	return events
}

// importInto runs an import and returns the report it printed.
func importInto(t *testing.T, log eventlog.UniqueAppender, events []domain.Event, dryRun bool) (string, error) {
	t.Helper()

	var report bytes.Buffer
	opts := &options{File: samplePath, DryRun: dryRun}
	err := importEvents(t.Context(), log, events, opts, &report, slog.New(slog.DiscardHandler))

	return report.String(), err
}

// mustImport runs an import that is expected to succeed.
func mustImport(t *testing.T, log eventlog.UniqueAppender, events []domain.Event, dryRun bool) string {
	t.Helper()

	report, err := importInto(t, log, events, dryRun)
	if err != nil {
		t.Fatalf("importEvents: %v\n%s", err, report)
	}
	return report
}

// TestImportAppendsEveryRow is the first run: an empty log, and a file that
// ends up in it whole.
func TestImportAppendsEveryRow(t *testing.T) {
	log := eventlog.NewMemory()
	events := sampleEvents(t)

	report := mustImport(t, log, events, false)

	stored := loadAll(t, log)
	if len(stored) != len(events) {
		t.Fatalf("the log holds %d events, want the %d rows of the file", len(stored), len(events))
	}

	// Under their keys, which is what a re-run will look for. An event stored
	// under an ID of the store's choosing would be an event no re-run could
	// recognize, and the import would append it again every time.
	byKey := make(map[string]domain.Event, len(stored))
	for _, e := range stored {
		byKey[e.ID] = e
	}
	for _, want := range events {
		got, ok := byKey[importKey(want)]
		if !ok {
			t.Errorf("no event in the log under the key for %s / %s", want.Month, want.Type)
			continue
		}
		if got.Amount != want.Amount {
			t.Errorf("%s / %s stored as %s, want %s", want.Month, want.Type, got.Amount, want.Amount)
		}
	}

	assertReportSays(t, report, "appended          7")
}

// TestImportIsIdempotent is the story: run it twice, and the second run appends
// nothing.
//
// It is the claim the whole design exists to make. The log is append-only, so a
// second copy of five years of history could not be deleted afterwards — the
// months would simply read wrong, permanently. Re-running is also not an
// exotic case to defend against: it is how an interrupted import is meant to be
// finished.
func TestImportIsIdempotent(t *testing.T) {
	log := eventlog.NewMemory()
	events := sampleEvents(t)

	mustImport(t, log, events, false)
	first := loadAll(t, log)

	report := mustImport(t, log, events, false)
	second := loadAll(t, log)

	if len(second) != len(first) {
		t.Fatalf("the log holds %d events after a second import, want the %d it held after the first", len(second), len(first))
	}

	// The same events, not merely the same number of them.
	for i := range first {
		if !equal(first[i], second[i]) {
			t.Errorf("event %d changed across the second import:\n\tbefore %+v\n\tafter  %+v", i, first[i], second[i])
		}
	}

	assertReportSays(t, report, "appended          0")
	assertReportSays(t, report, "already imported  7")
}

// TestImportResumesAPartialRun covers the reason a re-run happens at all: the
// first one stopped in the middle. What is missing is appended, what is there
// is not, and nobody has to work out which is which.
func TestImportResumesAPartialRun(t *testing.T) {
	log := eventlog.NewMemory()
	events := sampleEvents(t)

	// A first run that got three rows in before it died.
	for _, e := range events[:3] {
		if _, err := log.AppendUnique(t.Context(), importKey(e), e); err != nil {
			t.Fatalf("seeding a partial import: %v", err)
		}
	}

	report := mustImport(t, log, events, false)

	if got := loadAll(t, log); len(got) != len(events) {
		t.Fatalf("the log holds %d events after the resumed import, want %d", len(got), len(events))
	}
	assertReportSays(t, report, "appended          4")
	assertReportSays(t, report, "already imported  3")
}

// TestDryRunWritesNothing is the flag's whole promise.
func TestDryRunWritesNothing(t *testing.T) {
	log := eventlog.NewMemory()
	events := sampleEvents(t)

	report := mustImport(t, log, events, true)

	if got := loadAll(t, log); len(got) != 0 {
		t.Fatalf("a dry run left %d events in the log, want 0", len(got))
	}

	// And it says what it would have done: the months and types it discovered,
	// and the count of events it would append.
	for _, want := range []string{
		"dry run: nothing was written.",
		"rows              7",
		"months            2  2026-01 … 2026-02",
		"types             4  Groceries, Income, Monthly Spend, Rent",
		"to append         7",
	} {
		assertReportSays(t, report, want)
	}
}

// TestDryRunAfterAnImportProposesNothing is the dry run that answers "is this
// file already in?" — the question the owner asks before deciding whether to
// run the import again.
func TestDryRunAfterAnImportProposesNothing(t *testing.T) {
	log := eventlog.NewMemory()
	events := sampleEvents(t)

	mustImport(t, log, events, false)
	report := mustImport(t, log, events, true)

	assertReportSays(t, report, "to append         0")
	assertReportSays(t, report, "already imported  7")
}

// TestImportReportsADivergentCell covers the sheet that was edited after it was
// imported. The importer does not overwrite what it has already appended, and
// it does not keep quiet about it either.
func TestImportReportsADivergentCell(t *testing.T) {
	log := eventlog.NewMemory()
	events := sampleEvents(t)

	mustImport(t, log, events, false)

	// The owner edits January's rent in the sheet and runs the import again.
	edited := slicesClone(events)
	edited[0].Amount = money.Cents(125_000)

	report, err := importInto(t, log, edited, false)

	var unresolved *unresolvedError
	if !errors.As(err, &unresolved) {
		t.Fatalf("importEvents = %v, want an *unresolvedError so a script does not read the run as a clean import", err)
	}
	if unresolved.Divergent != 1 {
		t.Errorf("importEvents reported %d divergences, want 1", unresolved.Divergent)
	}

	// Nothing was written for it: not a second event, not an overwrite of the
	// first. The log still says what it said.
	stored := loadAll(t, log)
	if len(stored) != len(events) {
		t.Fatalf("the log holds %d events, want the %d it held before the edited re-import", len(stored), len(events))
	}

	imported, ok := find(stored, importKey(events[0]))
	if !ok {
		t.Fatal("the imported January rent is gone from the log")
	}
	if imported.Amount != events[0].Amount {
		t.Errorf("January rent is now %s, want the %s that was imported; the re-import overwrote it", imported.Amount, events[0].Amount)
	}

	// And the report names the row, both figures, and what to do.
	for _, want := range []string{
		"divergences (1)",
		"row 1     2026-01 / Rent (expense)",
		"log    $1,200.00",
		"sheet  $1,250.00",
		"Correct these in the app.",
	} {
		assertReportSays(t, report, want)
	}
}

// TestImportStillAppendsAroundADivergence: a divergence is one cell's problem,
// not the file's. The rows that are new still go in, so a sheet that grew a
// month and edited a figure is not stuck behind the figure.
func TestImportStillAppendsAroundADivergence(t *testing.T) {
	log := eventlog.NewMemory()
	events := sampleEvents(t)

	// Import all but the last row.
	mustImport(t, log, events[:len(events)-1], false)

	// Now the whole file, with one of the imported figures edited.
	edited := slicesClone(events)
	edited[0].Amount = money.Cents(125_000)

	report, err := importInto(t, log, edited, false)

	var unresolved *unresolvedError
	if !errors.As(err, &unresolved) {
		t.Fatalf("importEvents = %v, want a *divergenceError", err)
	}

	if got := loadAll(t, log); len(got) != len(events) {
		t.Errorf("the log holds %d events, want %d — the new row was held back by an unrelated cell's divergence", len(got), len(events))
	}
	assertReportSays(t, report, "appended          1")
	assertReportSays(t, report, "The rest of the file was imported.")
}

// TestImportDoesNotDisturbACorrectionMadeInTheApp is the property the dating
// rule and the key exist to protect, tested end to end.
//
// The owner imports the sheet, notices January's rent is wrong, and fixes it in
// the app. Later — a new laptop, a re-run to be sure, a second import of the
// same file — the importer runs again over the unchanged sheet. The correction
// has to survive that, and the month has to still read what the owner corrected
// it to.
func TestImportDoesNotDisturbACorrectionMadeInTheApp(t *testing.T) {
	log := eventlog.NewMemory()
	events := sampleEvents(t)

	mustImport(t, log, events, false)

	// The owner corrects January's rent in the app: an ordinary append, dated
	// now, under an ID the store chose.
	correction := domain.Event{
		Action:     domain.ActionSet,
		Month:      "2026-01",
		Type:       "Rent",
		Amount:     money.Cents(130_000),
		Direction:  domain.DirectionExpense,
		Note:       "the landlord raised it in January",
		RecordedAt: importedAt.Add(24 * time.Hour),
	}
	if _, err := log.Append(t.Context(), correction); err != nil {
		t.Fatalf("appending the correction: %v", err)
	}

	// And now the same sheet is imported again.
	mustImport(t, log, events, false)

	state, err := projection.Fold(loadAll(t, log))
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}

	got := state[projection.Key{Month: "2026-01", Type: "Rent", Direction: domain.DirectionExpense}]
	if got != correction.Amount {
		t.Errorf("January rent folds to %s, want the corrected %s; the re-import overrode a correction made after it", got, correction.Amount)
	}
}

// TestImportWillNotBuryAnEntryTheAppMadeFirst is the case that "a correction in
// the app lands last" does not cover, and the one that would lose the owner's
// own figure without a word.
//
// A replayed row is dated at the first instant of the month it belongs to. An
// entry made *during* that month is later than that, so the import sorts under
// it and the entry wins — which is the whole design. But the app lets the owner
// enter a month before it arrives, and next month's rent is exactly the sort of
// thing that gets entered early. That entry is dated *earlier* than the
// imported row, so the imported set would supersede it: the owner's $1,300
// silently replaced by the sheet's $1,200, in a log with no undo.
//
// The importer refuses the row instead, and says so.
func TestImportWillNotBuryAnEntryTheAppMadeFirst(t *testing.T) {
	log := eventlog.NewMemory()

	// On the 20th of June the owner enters July's rent in the app: the landlord
	// has told them what it will be.
	entered := domain.Event{
		Action:     domain.ActionSet,
		Month:      "2026-07",
		Type:       "Rent",
		Amount:     money.Cents(130_000),
		Direction:  domain.DirectionExpense,
		RecordedAt: time.Date(2026, time.June, 20, 9, 0, 0, 0, time.UTC),
	}
	if _, err := log.Append(t.Context(), entered); err != nil {
		t.Fatalf("appending the app's entry: %v", err)
	}

	// The sheet still says what it always said. The import runs on 14 July, so
	// this row is dated 2026-07-01 — after the entry above.
	sheet := []domain.Event{{
		Action:     domain.ActionSet,
		Month:      "2026-07",
		Type:       "Rent",
		Amount:     money.Cents(120_000),
		Direction:  domain.DirectionExpense,
		RecordedAt: time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC),
	}}

	report, err := importInto(t, log, sheet, false)

	var unresolved *unresolvedError
	if !errors.As(err, &unresolved) {
		t.Fatalf("importEvents = %v, want an *unresolvedError; the import buried an entry made in the app", err)
	}
	if unresolved.Conflicting != 1 {
		t.Errorf("importEvents reported %d conflicts, want 1", unresolved.Conflicting)
	}

	// Nothing was written, and the month still says what the owner said.
	if got := loadAll(t, log); len(got) != 1 {
		t.Fatalf("the log holds %d events, want the 1 the app appended", len(got))
	}

	state, err := projection.Fold(loadAll(t, log))
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	got := state[projection.Key{Month: "2026-07", Type: "Rent", Direction: domain.DirectionExpense}]
	if got != entered.Amount {
		t.Errorf("July rent folds to %s, want the %s entered in the app", got, entered.Amount)
	}

	for _, want := range []string{
		"conflicts (1)",
		"row 1     2026-07 / Rent (expense)",
		"app    $1,300.00",
		"sheet  $1,200.00",
	} {
		assertReportSays(t, report, want)
	}
}

// TestImportAppendsUnderAnEntryTheAppMadeLater is the other half of the rule,
// and the one that must keep working: an entry made during or after its month
// is *later* than the replayed row, so the row slots underneath it and is
// appended. Refusing this one too would make the importer useless the moment
// the app had been used at all.
func TestImportAppendsUnderAnEntryTheAppMadeLater(t *testing.T) {
	log := eventlog.NewMemory()

	entered := domain.Event{
		Action:     domain.ActionSet,
		Month:      "2026-01",
		Type:       "Rent",
		Amount:     money.Cents(130_000),
		Direction:  domain.DirectionExpense,
		RecordedAt: time.Date(2026, time.January, 15, 9, 0, 0, 0, time.UTC),
	}
	if _, err := log.Append(t.Context(), entered); err != nil {
		t.Fatalf("appending the app's entry: %v", err)
	}

	report := mustImport(t, log, sampleEvents(t), false)

	if got := loadAll(t, log); len(got) != 8 {
		t.Fatalf("the log holds %d events, want the app's 1 plus the file's 7", len(got))
	}
	assertReportSays(t, report, "appended          7")
	assertReportSays(t, report, "conflicting       0")

	// And the app's figure is still the one that counts: it was recorded after
	// the imported row is dated, so it folds last.
	state, err := projection.Fold(loadAll(t, log))
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	got := state[projection.Key{Month: "2026-01", Type: "Rent", Direction: domain.DirectionExpense}]
	if got != entered.Amount {
		t.Errorf("January rent folds to %s, want the %s entered in the app", got, entered.Amount)
	}
}

// TestImportReportsWhatTheMonthsFoldTo is the check the story asks for: after
// the import, the months are rolled up and printed, to be read against the
// sheet's own rollup columns.
//
// There is nothing to diff them against automatically, and deliberately so —
// `Monthly Bills Total`, `Income` and `Savings` were formulas in the sheet, and
// here they are recomputed from the events every time they are shown. A side
// file of expected totals would be a copy of a number the fold already derives,
// and a copy is a thing that can be wrong. So the report prints what the log
// folds to, and the owner reads it next to the spreadsheet.
func TestImportReportsWhatTheMonthsFoldTo(t *testing.T) {
	log := eventlog.NewMemory()
	report := mustImport(t, log, sampleEvents(t), false)

	// The sample's January: $1,200.00 rent + $245.67 groceries + $75.00 spend
	// against $4,000.00 of income.
	for _, want := range []string{
		"2026-01  $1,520.67  $4,000.00  $2,479.33",
		"2026-02  $1,280.00  $4,100.00  $2,820.00",
		"total  $2,800.67  $8,100.00  $5,299.33",
	} {
		assertReportSays(t, squashSpaces(report), squashSpaces(want))
	}
}

// TestImportRollsUpTheWholeLogNotJustTheFile: the rollups are what the months
// come to, and a month the owner has been entering by hand is still that month.
// Reporting only the imported rows would print totals that disagree with the
// app on the very screen the owner is checking them against.
func TestImportRollsUpTheWholeLogNotJustTheFile(t *testing.T) {
	log := eventlog.NewMemory()

	entered := domain.Event{
		Action:     domain.ActionAdd,
		Month:      "2026-01",
		Type:       "Coffee",
		Amount:     money.Cents(4_50),
		Direction:  domain.DirectionExpense,
		RecordedAt: importedAt.Add(24 * time.Hour),
	}
	if _, err := log.Append(t.Context(), entered); err != nil {
		t.Fatalf("appending an entry made in the app: %v", err)
	}

	report := mustImport(t, log, sampleEvents(t), false)

	// January's expenses are the sheet's $1,520.67 plus the $4.50 coffee.
	assertReportSays(t, squashSpaces(report), squashSpaces("2026-01  $1,525.17  $4,000.00  $2,474.83"))
}

// TestDryRunRollsUpWhatWouldHappen: the dry run's rollups are the point of it —
// the owner reads them against the sheet and *then* decides to import. Rolling
// up only what is already in the log would print zeros for a first import, and
// the flag would be useless for the one thing it is for.
func TestDryRunRollsUpWhatWouldHappen(t *testing.T) {
	log := eventlog.NewMemory()

	report := mustImport(t, log, sampleEvents(t), true)

	assertReportSays(t, report, "rollups: what the log would fold to")
	assertReportSays(t, squashSpaces(report), squashSpaces("2026-01  $1,520.67  $4,000.00  $2,479.33"))

	if got := loadAll(t, log); len(got) != 0 {
		t.Fatalf("the dry run wrote %d events while rolling them up", len(got))
	}
}

// TestImportOfAnEmptyLogAndAnEmptyFile: nothing in, nothing out, no error. A
// file with no rows is not an error — it is a conversion script that found
// nothing to convert, and the report says so rather than the importer refusing
// to run.
func TestImportOfAnEmptyFile(t *testing.T) {
	log := eventlog.NewMemory()

	report := mustImport(t, log, nil, true)

	assertReportSays(t, report, "rows              0")
	assertReportSays(t, report, "(the log is empty)")
}

// TestImportThatDiesMidwayStillReportsWhatItWrote is the worst day this binary
// has, and the day its output matters most.
//
// The appends that landed cannot be taken back — the log is append-only — so the
// one thing the operator needs is to be told what is now in it. A bare "import
// failed" on stderr, with nothing on stdout, would send them to the database to
// find out by hand.
func TestImportThatDiesMidwayStillReportsWhatItWrote(t *testing.T) {
	log := &failingLog{
		UniqueAppender: eventlog.NewMemory(),
		failAfter:      3,
		err:            errors.New("firestore: deadline exceeded"),
	}

	report, err := importInto(t, log, sampleEvents(t), false)
	if err == nil {
		t.Fatal("importEvents = nil, want the failure that stopped it")
	}

	// The three that landed are in the log, and are correct.
	if got := loadAll(t, log); len(got) != 3 {
		t.Fatalf("the log holds %d events, want the 3 that were appended before the failure", len(got))
	}

	for _, want := range []string{
		"appended          3",
		"The import did not finish: it stopped after 3 of 7 rows",
		// And it does not print rollups: the plan's projection assumed all
		// seven rows would land, so printing it would show months that add up
		// to the file rather than to the log — and the owner would check them
		// against the sheet and find them right.
		"(not computed: the import did not finish",
	} {
		assertReportSays(t, report, want)
	}
}

// TestImportCountsRowsAnotherRunAppended: a row that turned out to be in the
// log by the time this run reached it was not appended *by this run*, and the
// report must not claim it was. The count is what the owner reads to know what
// just happened.
func TestImportCountsRowsAnotherRunAppended(t *testing.T) {
	// A store that reports every key as taken — a concurrent importer that won
	// every race.
	log := &racingLog{UniqueAppender: eventlog.NewMemory()}

	report := mustImport(t, log, sampleEvents(t), false)

	assertReportSays(t, report, "appended          0")
	assertReportSays(t, report, "already imported  7")
	assertReportSays(t, report, "7 of those were appended by another run")
}

// failingLog is a log that breaks partway through the appends.
type failingLog struct {
	eventlog.UniqueAppender
	failAfter int
	err       error
	appended  int
}

func (l *failingLog) AppendUnique(ctx context.Context, key string, e domain.Event) (domain.Event, error) {
	if l.appended >= l.failAfter {
		return domain.Event{}, l.err
	}
	l.appended++
	return l.UniqueAppender.AppendUnique(ctx, key, e)
}

// racingLog is a log in which somebody else has already appended everything.
type racingLog struct {
	eventlog.UniqueAppender
}

func (l *racingLog) AppendUnique(context.Context, string, domain.Event) (domain.Event, error) {
	return domain.Event{}, fmt.Errorf("%w: someone else got there first", eventlog.ErrDuplicateKey)
}

// loadAll drains the log, failing the test on the first error.
func loadAll(t *testing.T, log eventlog.EventStore) []domain.Event {
	t.Helper()

	var events []domain.Event
	for e, err := range log.Load(context.WithoutCancel(t.Context())) {
		if err != nil {
			t.Fatalf("loading the log: %v", err)
		}
		events = append(events, e)
	}
	return events
}

// find returns the event stored under an ID.
func find(events []domain.Event, id string) (domain.Event, bool) {
	for _, e := range events {
		if e.ID == id {
			return e, true
		}
	}
	return domain.Event{}, false
}

// slicesClone copies the events so a test can edit one without editing the
// slice every other test in the file shares.
func slicesClone(events []domain.Event) []domain.Event {
	return append([]domain.Event(nil), events...)
}

// squashSpaces collapses runs of spaces, so that a test of the report's numbers
// is not also a test of the column widths tabwriter chose for them.
func squashSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func assertReportSays(t *testing.T, report, want string) {
	t.Helper()

	if !strings.Contains(report, want) {
		t.Errorf("the report does not contain %q:\n%s", want, report)
	}
}
