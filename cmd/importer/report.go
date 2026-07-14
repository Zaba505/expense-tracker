package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Zaba505/expense-tracker/internal/projection"
)

// The report is the importer's real output, and it goes to stdout as text
// meant for a person. The logger goes to stderr, in JSON, and says what the
// process is doing. Keeping them apart is what lets `importer --dry-run >
// report.txt` produce a file worth reading, and it is the split the two
// audiences already want: the report is read once, by the owner, deciding
// whether to run the import for real; the log is read by whatever is
// watching the run.

// writeReport prints what the import did, or — for a dry run — what it
// would do.
//
// Both cases print the same report, from the same plan, because they are
// the same plan. The only difference a dry run makes is that the appends did
// not happen, and the report says so rather than leaving the reader to
// remember which flag they passed.
func writeReport(w io.Writer, p *plan, res *result, dryRun bool) error {
	out := &errWriter{w: w}

	out.printf("importer: %s\n", p.Source)
	if dryRun {
		out.printf("dry run: nothing was written.\n")
	}

	out.printf("\nsource\n")
	out.printf("  rows              %d\n", len(p.Rows))
	out.printf("  months            %d  %s\n", len(p.Months), span(p.Months))
	out.printf("  types             %d  %s\n", len(p.Types), strings.Join(p.Types, ", "))

	out.printf("\nevents\n")
	// What a dry run would do, against what a real run did — and "did" is
	// counted from the appends that returned, not from the rows that were
	// planned. A run that failed at row 400 of 900 must not print 900, which is
	// the number it set out to write and not the number it wrote.
	if dryRun {
		out.printf("  %-16s  %d\n", "to append", len(p.Pending))
	} else {
		out.printf("  %-16s  %d\n", "appended", res.Appended)
	}
	out.printf("  %-16s  %d\n", "already imported", len(p.Imported)+res.Raced)
	out.printf("  %-16s  %d\n", "divergent", len(p.Divergent))
	out.printf("  %-16s  %d\n", "conflicting", len(p.Conflicting))

	if res.Raced > 0 {
		out.printf("\n  %d of those were appended by another run of the importer while this one\n  was working. They are in the log; this run did not put them there.\n", res.Raced)
	}

	if res.Stopped {
		// The most important sentence in the report, on the worst day it has.
		// What is in the log is in the log — there is no rolling it back — so
		// the only useful thing to say is that it is correct as far as it got,
		// and that finishing the job is a re-run and not a repair.
		out.printf("\nThe import did not finish: it stopped after %d of %d rows, and the rest were\nnot attempted. What was appended is correct and will not be appended again —\nfix what broke and re-run to finish.\n", res.Appended, len(p.Pending))
	}

	writeDivergences(out, p, dryRun)
	writeConflicts(out, p)
	writeRollups(out, res, dryRun)

	return out.err
}

// writeConflicts names the cells the app recorded before the import could, and
// that an imported row would therefore have buried.
//
// It is the section that stops the importer from quietly winning an argument it
// was never meant to be in. See outcomeConflict for why an app entry can end up
// underneath a replayed row: the row is dated at the month it belongs to, and
// an entry made before that month began is older than it.
func writeConflicts(out *errWriter, p *plan) {
	if len(p.Conflicting) == 0 {
		return
	}

	out.printf("\nconflicts (%d)\nThe app already recorded these cells, and it recorded them early enough that\nimporting the sheet's row would have superseded them. Nothing was written for\nthem — the sheet does not get to overrule what you entered.\n\n", len(p.Conflicting))

	for _, row := range p.Conflicting {
		out.printf("  row %-4d  %s / %s (%s)\n", row.Row, row.Event.Month, row.Event.Type, row.Event.Direction)
		out.printf("            app    %s  recorded %s\n", row.Stored.Amount.Display(), row.Stored.RecordedAt.Format(time.RFC3339))
		out.printf("            sheet  %s  would be dated %s\n", row.Event.Amount.Display(), row.Event.RecordedAt.Format(time.RFC3339))
	}

	out.printf("\nIf the sheet is right, enter its figure in the app: an entry made now is dated\nnow, and lands last. If the app is right, there is nothing to do — the cell is\nalready what you meant it to be, and the importer will keep leaving it alone.\n")
}

// writeDivergences names the cells whose figure in the sheet is no longer
// the figure in the log, and says what to do about it.
//
// It is the report's most important section, and the one that is usually
// empty. A divergence is not a failure of the import — it is the import
// declining to argue with the log about the past — but it is the owner's to
// resolve, and a summary that only counted them would leave them to go
// looking for which ones.
func writeDivergences(out *errWriter, p *plan, dryRun bool) {
	if len(p.Divergent) == 0 {
		return
	}

	out.printf("\ndivergences (%d)\nThe log already holds these cells, with a different amount. Nothing was\nwritten for them.\n\n", len(p.Divergent))

	for _, row := range p.Divergent {
		out.printf("  row %-4d  %s / %s (%s)\n", row.Row, row.Event.Month, row.Event.Type, row.Event.Direction)
		out.printf("            log    %s\n", row.Stored.Amount.Display())
		out.printf("            sheet  %s\n", row.Event.Amount.Display())
	}

	// The way out is the app, not another run of this. Said here because this
	// is where it is read, and because the obvious guess — edit the sheet
	// back, or re-run with some flag — is the wrong one, and there is no flag.
	out.printf("\nCorrect these in the app. The importer will not overwrite an event it has\nalready appended: a replayed row is dated at the month it belongs to, so a\nsecond one would tie with the first and the fold would pick between them by\nID. A correction made in the app is dated when it is made, and so lands last.\n")

	if !dryRun {
		out.printf("The rest of the file was imported.\n")
	}
}

// writeRollups prints what each month folds to, for checking against the
// sheet's own rollup columns.
//
// This is the check the story asks for, and it is deliberately a table for a
// human rather than a pass/fail against a machine-readable copy of the
// sheet's totals. The tracker stores no rollups — `Monthly Bills Total`,
// `Income`, `Savings` were formulas, and here they are recomputed from the
// events on every read — so there is no second source of truth to diff
// against, and inventing one (a side file from the conversion script) would
// mean maintaining a copy of a number the fold already derives. What the
// owner wants to know is whether the months add up to what the spreadsheet
// said, and the way to know is to read them next to it.
func writeRollups(out *errWriter, res *result, dryRun bool) {
	folds := "folds"
	if dryRun {
		folds = "would fold"
	}
	out.printf("\nrollups: what the log %s to, to check against the sheet\n", folds)

	if !res.RollupsKnown {
		// Said rather than guessed. The plan's projection assumed every pending
		// row would land; printing it after a run that stopped halfway would
		// print months that add up to the file rather than to the log, and the
		// owner would check them against the sheet and find them right.
		out.printf("  (not computed: the import did not finish, so what the log now holds\n   is not what this run set out to write. Re-run, then read them.)\n")
		return
	}

	if len(res.Rollups) == 0 {
		out.printf("  (the log is empty)\n")
		return
	}

	rows := make([][4]string, 0, len(res.Rollups)+2)
	rows = append(rows, [4]string{"month", "expenses", "income", "net"})
	for _, r := range res.Rollups {
		rows = append(rows, [4]string{r.Month, r.Expenses.Display(), r.Income.Display(), r.Net().Display()})
	}

	total := projection.Total(res.Rollups)
	rows = append(rows, [4]string{"total", total.Expenses.Display(), total.Income.Display(), total.Net().Display()})

	// Measured before anything is printed, because a column is only as wide as
	// its widest figure and the widest figure might be in the last month. The
	// alternative — text/tabwriter — aligns every column the same way, and
	// these do not want the same alignment: the months read down the left edge,
	// and the money reads by its decimal point on the right.
	var width [4]int
	for _, row := range rows {
		for i, cell := range row {
			width[i] = max(width[i], len(cell))
		}
	}

	for i, row := range rows {
		out.printf("  %-*s", width[0], row[0])
		for column, cell := range row[1:] {
			out.printf("  %*s", width[column+1], cell)
		}
		out.printf("\n")

		// Under the header, and above the total: the two places the eye needs
		// to be told that the rows below are a different kind of thing.
		if i == 0 || i == len(rows)-2 {
			out.printf("  %s\n", rule(width))
		}
	}
}

// rule is the line between the header and the months, and between the months
// and the total: a dash under each column, the width of that column.
func rule(width [4]int) string {
	var b strings.Builder
	for i, w := range width {
		if i > 0 {
			b.WriteString("  ")
		}
		b.WriteString(strings.Repeat("-", w))
	}
	return b.String()
}

// span is the range a sorted set of months covers, for a summary line that
// should say "2021-01 … 2025-12" rather than list sixty of them.
func span(months []string) string {
	switch len(months) {
	case 0:
		return ""
	case 1:
		return months[0]
	default:
		return months[0] + " … " + months[len(months)-1]
	}
}

// errWriter is a writer that remembers its first failure, so that a report
// built from thirty prints is checked once instead of thirty times.
//
// The failure is worth catching at all — a report is only stdout — because
// stdout can be a file or a pipe, and an import that says it appended nine
// hundred events into a report that was truncated at the first month has
// told the owner nothing they can act on.
type errWriter struct {
	w   io.Writer
	err error
}

func (w *errWriter) printf(format string, args ...any) {
	if w.err != nil {
		return
	}
	if _, err := fmt.Fprintf(w.w, format, args...); err != nil {
		w.fail(err)
	}
}

func (w *errWriter) fail(err error) {
	if w.err == nil {
		w.err = fmt.Errorf("importer: writing the report: %w", err)
	}
}
