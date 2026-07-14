// Command importer translates the owner's exported expense spreadsheet into an
// event stream appended to the event log.
//
// Its input is Parquet, not the spreadsheet's own CSV export. A conversion
// script — the owner's, ad hoc, run once — unpivots the sheet into one row per
// event and says what each row is. That is the thing the spreadsheet cannot say
// for itself: a column heading does not tell the importer whether the number
// under it is a bill, a paycheck, or a formula, so a wide export leaves it
// guessing, silently, into a log that cannot be edited afterwards. Parquet moves
// the unpivoting and the classification into the script, where the sheet's
// context actually is, and leaves the importer a schema it can check.
//
// The input schema, one row per event:
//
//	month         STRING  the calendar month, zero-padded: "2026-01"
//	type          STRING  the type, as it should read: "Rent"
//	amount_cents  INT64   the amount, in whole cents: 120000 is $1,200.00
//	direction     STRING  "expense" or "income"
//
// All four are required. Columns beyond them are ignored, so a script is free to
// carry provenance. Rollup and formula columns are not events and simply have no
// rows — there is nothing here for the importer to ignore, and so nothing for it
// to get wrong. Every row becomes a set event, because a spreadsheet cell is a
// total rather than a transaction.
//
// amount_cents is an integer for a reason worth stating: dollars written as a
// float truncate into it a hundredfold light, and every value would still look
// like a plausible number of cents. The importer checks the column's type before
// it reads a row.
//
// # Running it
//
//	importer -file history.parquet -dry-run    # what it would do
//	importer -file history.parquet             # do it
//
// The project comes from -project or GCP_PROJECT. Set -emulator-host, or
// FIRESTORE_EMULATOR_HOST, to point it at a local emulator instead of the live
// database:
//
//	dagger call emulator up --ports 8085:8085
//	FIRESTORE_EMULATOR_HOST=localhost:8085 GCP_PROJECT=expense-tracker \
//	  go run ./cmd/importer -file history.parquet -dry-run
//
// Against Cloud Firestore it authenticates with Application Default Credentials
// and needs nothing else. The same binary and the same code path serve both:
// the only difference is which address the client dials.
//
// # Re-running it
//
// Re-running is safe, and is the expected way to finish an import that was
// interrupted — a laptop that slept, a network that dropped, a run that was
// stopped halfway. Every row is appended under a key derived from the cell it
// came from (see key.go), and the log takes a key once, so a re-run appends
// what is missing and skips what is not. There is no state file to keep, and no
// way for one to be wrong: the record of what has been imported is the log.
//
// What a re-run will not do is overwrite. A row whose amount was edited in the
// sheet after it was imported is reported as a divergence and left alone, and a
// row whose cell the app got to first is reported as a conflict and left alone —
// the importer never argues with the log about the past. Settling either is the
// app's job, where an entry is dated when it is made and therefore lands last.
// See the outcome constants in plan.go.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"time"

	// Same reason as cmd/server: this ships in a scratch image, which has
	// no CA certificates, and appending the imported events means TLS to
	// Firestore. A missing trust store does not fail at build time — it
	// fails at the first handshake, in whatever environment runs it first.
	_ "golang.org/x/crypto/x509roots/fallback"

	"github.com/Zaba505/expense-tracker/internal/domain"
	"github.com/Zaba505/expense-tracker/internal/eventlog"
	"github.com/Zaba505/expense-tracker/internal/projection"
)

func main() {
	// The log goes to stderr and the report to stdout. They have different
	// readers: the report is read once, by the owner, deciding whether the
	// months add up; the log is read by whatever is watching the run. It also
	// means `importer -dry-run > report.txt` writes a report and not a
	// report interleaved with JSON.
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	// Interrupt cancels the run rather than killing it, so a ctrl-C between
	// two appends stops between two appends. Nothing needs unwinding — the
	// events already appended are correct, and the next run picks up the rest
	// — but a cancelled context is how the store is told to stop, and how the
	// report gets to say what was done before it stopped.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	err := run(ctx, os.Args[1:], os.Stdout, os.Stderr, logger)
	switch {
	case err == nil:
	case errors.Is(err, flag.ErrHelp):
		// -h asked for the usage and got it. That is not a failure.
	default:
		logger.Error("import failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

// options is what the importer was asked to do.
type options struct {
	// File is the converted spreadsheet, in Parquet.
	File string

	// DryRun stops the run before it writes anything.
	DryRun bool

	// Project owns the Firestore database, and EmulatorHost — when set —
	// replaces it with a local emulator.
	Project      string
	EmulatorHost string
}

// run is main with its dependencies passed in, so that a test can drive the
// whole flag-parsing-to-report path without a process.
func run(ctx context.Context, args []string, stdout, stderr io.Writer, logger *slog.Logger) error {
	opts, err := parseFlags(args, os.Getenv, stderr)
	if err != nil {
		return err
	}

	// Parsed before the store is opened, so a file the importer cannot read is
	// reported without a database ever being dialed. It is also the half of
	// the work that catches the most: a missing column, a float where cents
	// were promised, two rows for one cell.
	events, err := parseFile(opts.File, time.Now())
	if err != nil {
		return err
	}

	store, err := eventlog.New(ctx, eventlog.Options{
		ProjectID:    opts.Project,
		EmulatorHost: opts.EmulatorHost,
	})
	if err != nil {
		return err
	}
	defer func() {
		if err := store.Close(); err != nil {
			logger.ErrorContext(ctx, "closing the event log", slog.String("error", err.Error()))
		}
	}()

	return importEvents(ctx, store, events, opts, stdout, logger)
}

// importEvents is the importer: plan, append what is missing, report.
//
// It takes the log as the interface rather than the store so that every test
// below it runs against eventlog.Memory — the same log, the same refusals, the
// same idempotence, without a database. What it does to a Memory is what it
// does to Firestore, which is the only reason a test of an import that cannot
// be undone is worth anything.
func importEvents(
	ctx context.Context,
	log eventlog.UniqueAppender,
	events []domain.Event,
	opts *options,
	out io.Writer,
	logger *slog.Logger,
) error {
	stored, err := loadLog(ctx, log)
	if err != nil {
		return err
	}

	p, err := makePlan(opts.File, events, stored)
	if err != nil {
		return err
	}

	// A dry run's result is the plan's own projection: nothing was appended,
	// and the rollups are the ones the log would fold to.
	res := &result{Rollups: p.Rollups, RollupsKnown: true}

	var appendErr error
	if !opts.DryRun {
		res.Appended, res.Raced, appendErr = appendPending(ctx, log, p, logger)
		res.Stopped = appendErr != nil

		// Rolled up from what the database now holds, read back, rather than
		// from what the plan predicted it would hold. A prediction checked
		// against the sheet would only prove the importer agrees with itself;
		// this proves the events are in the log, and folds the same events the
		// app will fold when it renders the months.
		//
		// Not attempted after a failed append, and not faked either: the plan's
		// projection assumed every pending row would land, and printing it now
		// would print months that add up to a file rather than to the log. The
		// report says the rollups are unknown, which is the true answer and the
		// one that sends the reader back here to re-run.
		res.RollupsKnown = false
		res.Rollups = nil

		if appendErr == nil {
			reloaded, err := loadLog(ctx, log)
			if err != nil {
				appendErr = err
			} else if rollups, err := rollupsAfter(reloaded, nil); err != nil {
				appendErr = err
			} else {
				res.Rollups, res.RollupsKnown = rollups, true
			}
		}
	}

	// The report is written whatever happened, and before anything is returned.
	// A run that died at row 400 of 900 left 399 events in a log that cannot be
	// edited, and the one thing its operator needs is to be told that — an
	// error on stderr and nothing on stdout would leave them to go and find out
	// from the database.
	reportErr := writeReport(out, p, res, opts.DryRun)

	switch {
	case appendErr != nil:
		// Outranks a failure to write the report: one of them is events in the
		// log, the other is text on a terminal.
		return appendErr
	case reportErr != nil:
		return reportErr
	case len(p.Divergent) > 0 || len(p.Conflicting) > 0:
		// Returned after the report, so the reader has the detail in front of
		// them, and returned at all so that a script running this does not read
		// "the sheet and the log disagree" as success.
		return &unresolvedError{Divergent: len(p.Divergent), Conflicting: len(p.Conflicting)}
	}
	return nil
}

// result is what carrying the plan out actually did, as opposed to what it
// proposed to do. The two are the same on a good day and the difference is the
// whole point on a bad one.
type result struct {
	// Appended is the number of rows this run put in the log.
	Appended int

	// Raced is the number of pending rows that turned out to be in the log by
	// the time this run tried to append them — another importer got there
	// first. The row is in the log either way, which is all the importer
	// wanted, so it is not a failure; it is just not something this run did.
	Raced int

	// Stopped says the appends did not finish. The rows before the failure are
	// in the log and are correct; the rows after it were not attempted.
	Stopped bool

	// Rollups is what the log folds to, and RollupsKnown says whether anyone
	// managed to find out. A run that could not read the log back has no
	// business printing months.
	Rollups      []projection.MonthRollup
	RollupsKnown bool
}

// appendPending appends the rows the log has never seen, and nothing else.
//
// One row at a time, and not batched. Five years of a household's spreadsheet
// is a few hundred rows, run once, so the round trips cost seconds — and what
// they buy is that a failure names the row it failed on, and that a run which
// dies halfway leaves every row it did append already correct and already
// keyed. A batch would trade both away for a wait nobody is having.
// It returns what it managed to do — the rows it appended, and the rows it
// found another run had already appended — alongside the failure that stopped
// it, if one did. The counts are what the report prints: "appended" has to mean
// the rows that are in the log because of this run, not the rows this run
// intended to put there, or a run that died at row 400 of 900 would print 900.
func appendPending(ctx context.Context, log eventlog.UniqueAppender, p *plan, logger *slog.Logger) (appended, raced int, err error) {
	if len(p.Pending) == 0 {
		logger.InfoContext(ctx, "nothing to append", slog.Int("already_imported", len(p.Imported)))
		return 0, 0, nil
	}

	logger.InfoContext(ctx, "appending events",
		slog.Int("pending", len(p.Pending)),
		slog.Int("already_imported", len(p.Imported)),
	)

	for _, row := range p.Pending {
		_, err := log.AppendUnique(ctx, row.Key, row.Event)

		switch {
		case err == nil:
			appended++
		case errors.Is(err, eventlog.ErrDuplicateKey):
			// The key was taken between the plan and this write: a second
			// importer, or a run racing the one that is retrying it. This is
			// the idempotence doing its job rather than failing at it — the
			// row is in the log, which is all the importer wanted — so the run
			// carries on. It is counted apart from the rows this run appended,
			// because it is not one of them.
			raced++
			logger.WarnContext(ctx, "row was appended by another run while this one was working",
				slog.Int("row", row.Row),
				slog.String("month", row.Event.Month),
				slog.String("type", row.Event.Type),
			)
		default:
			// Stops at the first failure rather than pressing on. A log that
			// cannot be written to will not start working three rows later,
			// and every row that did land is already correct and already
			// keyed — so the next run resumes from here rather than starting
			// over.
			return appended, raced, fmt.Errorf("importer: appending row %d (%s / %s), after %d of %d: %w",
				row.Row, row.Event.Month, row.Event.Type, appended, len(p.Pending), err)
		}
	}

	logger.InfoContext(ctx, "appended events", slog.Int("appended", appended), slog.Int("raced", raced))
	return appended, raced, nil
}

// loadLog drains the log into a slice, in the log's order.
//
// The first error ends the load and is returned. A partially-read log is not a
// smaller log, it is a wrong one: the importer decides what to append by what
// it finds already there, and a load that quietly stopped short would leave it
// certain that rows it has already imported are missing.
func loadLog(ctx context.Context, log eventlog.EventStore) ([]domain.Event, error) {
	var events []domain.Event
	for e, err := range log.Load(ctx) {
		if err != nil {
			return nil, fmt.Errorf("importer: loading the log: %w", err)
		}
		events = append(events, e)
	}
	return events, nil
}

// parseFile reads the converted spreadsheet.
func parseFile(path string, now time.Time) ([]domain.Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("importer: opening %s: %w", path, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("importer: reading %s: %w", path, err)
	}

	return parseParquet(f, info.Size(), now)
}

// parseFlags reads the command line, falling back to the environment for the
// two values the server also takes from it.
//
// It does not go through internal/config, and the reason is worth stating:
// config.Load demands the OAuth credentials and the session key, and demands
// them because a server that cannot complete a sign-in is a server nobody can
// use. The importer signs nobody in. Relaxing those to optional to let this
// binary through would weaken the check where it matters — on the server, at
// startup, which is the one place it is meant to be uncompromising.
func parseFlags(args []string, getenv func(string) string, stderr io.Writer) (*options, error) {
	flags := flag.NewFlagSet("importer", flag.ContinueOnError)
	flags.SetOutput(stderr)

	opts := &options{}
	flags.StringVar(&opts.File, "file", "", "the converted spreadsheet, in Parquet (required)")
	flags.BoolVar(&opts.DryRun, "dry-run", false, "report what the import would do, and write nothing")
	flags.StringVar(&opts.Project, "project", getenv("GCP_PROJECT"), "the Google Cloud project owning the Firestore database (default $GCP_PROJECT)")
	flags.StringVar(&opts.EmulatorHost, "emulator-host", getenv("FIRESTORE_EMULATOR_HOST"), "host:port of a Firestore emulator to use instead of the live database (default $FIRESTORE_EMULATOR_HOST)")

	flags.Usage = func() {
		fmt.Fprintf(stderr, "Usage: importer -file <parquet> [-dry-run]\n\nAppends the converted spreadsheet to the event log. Re-runs are safe: a row\nalready imported is skipped, not appended twice.\n\n")
		flags.PrintDefaults()
	}

	if err := flags.Parse(args); err != nil {
		return nil, err
	}

	// Both at once, so a first run with neither set is told both things rather
	// than one of them and then, on the next attempt, the other.
	var missing []error
	if opts.File == "" {
		missing = append(missing, errors.New("-file is required: the converted spreadsheet, in Parquet"))
	}
	if opts.Project == "" {
		missing = append(missing, errors.New("-project is required, or set GCP_PROJECT"))
	}
	if len(missing) > 0 {
		flags.Usage()
		return nil, fmt.Errorf("importer: %w", errors.Join(missing...))
	}

	return opts, nil
}

// unresolvedError is the run that finished, and left something for the owner:
// cells where the sheet and the log disagree (Divergent), and cells the app got
// to first (Conflicting).
//
// It is an error because it is a thing somebody has to deal with, and a run
// that exited 0 would tell a script that the sheet and the log agree. It is not
// a failed import: everything that could be appended was, and re-running
// changes nothing until the disagreement is settled in the app.
type unresolvedError struct {
	Divergent   int
	Conflicting int
}

func (e *unresolvedError) Error() string {
	var parts []string
	if e.Divergent > 0 {
		parts = append(parts, fmt.Sprintf("%d cell(s) in the sheet disagree with what was imported", e.Divergent))
	}
	if e.Conflicting > 0 {
		parts = append(parts, fmt.Sprintf("%d cell(s) were already recorded in the app", e.Conflicting))
	}
	return fmt.Sprintf("importer: %s; they were left alone — settle them in the app", strings.Join(parts, ", and "))
}
