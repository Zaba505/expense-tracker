package main

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"

	"github.com/Zaba505/expense-tracker/internal/domain"
)

// update regenerates testdata/sample.parquet from sampleRows:
//
//	go test ./cmd/importer -update
//
// The sample is committed rather than built on the fly so that the test reads a
// real Parquet file — the footer, the encodings, the types a writer actually
// chose — rather than one this test wrote to its own expectations.
var update = flag.Bool("update", false, "regenerate testdata/sample.parquet")

// samplePath is the golden input: one month of bills, a second month with a gap
// in it, income in both. It is the converted spreadsheet in miniature.
const samplePath = "testdata/sample.parquet"

var sampleRows = []row{
	{Month: "2026-01", Type: "Rent", AmountCents: 120_000, Direction: "expense"},
	{Month: "2026-01", Type: "Groceries", AmountCents: 24_567, Direction: "expense"},
	{Month: "2026-01", Type: "Monthly Spend", AmountCents: 7_500, Direction: "expense"},
	{Month: "2026-01", Type: "Income", AmountCents: 400_000, Direction: "income"},
	{Month: "2026-02", Type: "Rent", AmountCents: 120_000, Direction: "expense"},
	{Month: "2026-02", Type: "Monthly Spend", AmountCents: 8_000, Direction: "expense"},
	{Month: "2026-02", Type: "Income", AmountCents: 410_000, Direction: "income"},
}

// importedAt is when the import runs, in every test that does not care when.
// It is after every month in sampleRows, so nothing in the sample is clamped —
// TestParseParquetClampsFutureMonths is where that happens.
var importedAt = time.Date(2026, time.July, 14, 9, 30, 0, 0, time.UTC)

func TestParseParquet(t *testing.T) {
	if *update {
		writeSample(t)
	}

	f, err := os.Open(samplePath)
	if err != nil {
		t.Fatalf("Open(%s): %v", samplePath, err)
	}
	t.Cleanup(func() { _ = f.Close() })

	info, err := f.Stat()
	if err != nil {
		t.Fatalf("Stat(%s): %v", samplePath, err)
	}

	got, err := parseParquet(f, info.Size(), importedAt)
	if err != nil {
		t.Fatalf("parseParquet(%s): %v", samplePath, err)
	}

	// Every event is a set: a spreadsheet cell is a total, not a transaction.
	//
	// The microsecond offsets run 0..6 across the whole file rather than
	// restarting each month, because the sequence is the row's position in it.
	// Only the order within one month and type has to hold, and a single
	// counter gives that for free — while also keeping rows apart when two
	// months clamp to the same instant.
	jan := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	feb := time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC)
	want := []domain.Event{
		{Action: domain.ActionSet, Month: "2026-01", Type: "Rent", Amount: 120_000, Direction: domain.DirectionExpense, RecordedAt: jan},
		{Action: domain.ActionSet, Month: "2026-01", Type: "Groceries", Amount: 24_567, Direction: domain.DirectionExpense, RecordedAt: jan.Add(1 * time.Microsecond)},
		{Action: domain.ActionSet, Month: "2026-01", Type: "Monthly Spend", Amount: 7_500, Direction: domain.DirectionExpense, RecordedAt: jan.Add(2 * time.Microsecond)},
		{Action: domain.ActionSet, Month: "2026-01", Type: "Income", Amount: 400_000, Direction: domain.DirectionIncome, RecordedAt: jan.Add(3 * time.Microsecond)},
		{Action: domain.ActionSet, Month: "2026-02", Type: "Rent", Amount: 120_000, Direction: domain.DirectionExpense, RecordedAt: feb.Add(4 * time.Microsecond)},
		{Action: domain.ActionSet, Month: "2026-02", Type: "Monthly Spend", Amount: 8_000, Direction: domain.DirectionExpense, RecordedAt: feb.Add(5 * time.Microsecond)},
		{Action: domain.ActionSet, Month: "2026-02", Type: "Income", Amount: 410_000, Direction: domain.DirectionIncome, RecordedAt: feb.Add(6 * time.Microsecond)},
	}

	assertEvents(t, got, want)
}

// TestParseParquetChecksSchema covers what parquet-go will not: it zero-fills a
// column the file does not have, and coerces one whose type is wrong. Every case
// here decodes without complaint unless the schema is checked first, and each is
// a way a conversion script gets it wrong — permanently, since the log it feeds
// is append-only. See inputColumns.
func TestParseParquetChecksSchema(t *testing.T) {
	t.Run("a missing direction column is not an expense", func(t *testing.T) {
		// Decodes as "", which Normalize would read as an expense: every
		// paycheck in the file, filed as a bill.
		type noDirection struct {
			Month       string `parquet:"month"`
			Type        string `parquet:"type"`
			AmountCents int64  `parquet:"amount_cents"`
		}
		r := writeParquet(t, []noDirection{{"2026-01", "Income", 400_000}})

		_, err := parseParquet(r, r.Size(), importedAt)
		assertErrorContains(t, err, `missing the "direction" column`)
	})

	t.Run("dollars as a float are refused, not truncated", func(t *testing.T) {
		// The mistake a conversion script makes by default. 1200.00 dollars
		// truncates into an int64 as 1200 — $12.00, a hundredfold light, and a
		// perfectly plausible number of cents, so nothing downstream could tell.
		type floatAmount struct {
			Month       string  `parquet:"month"`
			Type        string  `parquet:"type"`
			AmountCents float64 `parquet:"amount_cents"`
			Direction   string  `parquet:"direction"`
		}
		r := writeParquet(t, []floatAmount{{"2026-01", "Rent", 1200.00, "expense"}})

		_, err := parseParquet(r, r.Size(), importedAt)
		assertErrorContains(t, err, `column "amount_cents" is DOUBLE, want INT64`)
	})

	t.Run("a mistyped direction column is refused", func(t *testing.T) {
		type intDirection struct {
			Month       string `parquet:"month"`
			Type        string `parquet:"type"`
			AmountCents int64  `parquet:"amount_cents"`
			Direction   int64  `parquet:"direction"`
		}
		r := writeParquet(t, []intDirection{{"2026-01", "Rent", 120_000, 7}})

		_, err := parseParquet(r, r.Size(), importedAt)
		assertErrorContains(t, err, `column "direction" is INT64, want BYTE_ARRAY`)
	})

	t.Run("columns the importer has no opinion about are ignored", func(t *testing.T) {
		// A script is free to carry provenance — which cell a row came from.
		type withProvenance struct {
			Month       string `parquet:"month"`
			Type        string `parquet:"type"`
			AmountCents int64  `parquet:"amount_cents"`
			Direction   string `parquet:"direction"`
			SourceCell  string `parquet:"source_cell"`
		}
		r := writeParquet(t, []withProvenance{{"2026-01", "Rent", 120_000, "expense", "B7"}})

		got, err := parseParquet(r, r.Size(), importedAt)
		if err != nil {
			t.Fatalf("parseParquet() = %v, want the extra column ignored", err)
		}

		want := []domain.Event{{
			Action:     domain.ActionSet,
			Month:      "2026-01",
			Type:       "Rent",
			Amount:     120_000,
			Direction:  domain.DirectionExpense,
			RecordedAt: time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
		}}
		assertEvents(t, got, want)
	})
}

// TestParseParquetReportsTheRowAtFault covers the rows a well-formed file can
// still carry. An import is not an entry form: nothing here gets to default, and
// a row the domain would refuse is refused before it can be appended.
func TestParseParquetReportsTheRowAtFault(t *testing.T) {
	tests := []struct {
		name   string
		rows   []row
		row    int
		column string
	}{
		{
			name:   "a month that is not a calendar month",
			rows:   []row{{Month: "2026-01", Type: "Rent", AmountCents: 120_000, Direction: "expense"}, {Month: "January 2026", Type: "Rent", AmountCents: 120_000, Direction: "expense"}},
			row:    2,
			column: "month",
		},
		{
			// "2026-7" sorts after December, so the month has to be zero-padded.
			name:   "a month that is not zero-padded",
			rows:   []row{{Month: "2026-7", Type: "Rent", AmountCents: 120_000, Direction: "expense"}},
			row:    1,
			column: "month",
		},
		{
			name:   "a direction that is neither expense nor income",
			rows:   []row{{Month: "2026-01", Type: "Rent", AmountCents: 120_000, Direction: "outgoing"}},
			row:    1,
			column: "direction",
		},
		{
			// The row said nothing, so the importer refuses to say expense for it.
			name:   "a direction left empty",
			rows:   []row{{Month: "2026-01", Type: "Rent", AmountCents: 120_000, Direction: ""}},
			row:    1,
			column: "direction",
		},
		{
			name:   "a type left empty",
			rows:   []row{{Month: "2026-01", Type: "   ", AmountCents: 120_000, Direction: "expense"}},
			row:    1,
			column: "type",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r := writeParquet(t, test.rows)

			_, err := parseParquet(r, r.Size(), importedAt)
			if err == nil {
				t.Fatalf("parseParquet() = nil, want a row error")
			}

			var rowErr *rowError
			if !errors.As(err, &rowErr) {
				t.Fatalf("parseParquet() = %T (%v), want *rowError", err, err)
			}
			if rowErr.Row != test.row || rowErr.Column != test.column {
				t.Fatalf("parseParquet() = %+v, want row %d column %q", rowErr, test.row, test.column)
			}
		})
	}
}

// TestParseParquetClampsFutureMonths pins the rule that keeps an import from
// overwriting a correction made after it. The sheet has rows for months that
// have not happened yet — next month's rent is already in it — and dating those
// at the month they name would stamp them in the future, ahead of any edit the
// owner makes before the month arrives. A set supersedes the sets before it, so
// the import would win and the edit would vanish until the month came around.
func TestParseParquetClampsFutureMonths(t *testing.T) {
	r := writeParquet(t, []row{
		{Month: "2026-06", Type: "Rent", AmountCents: 120_000, Direction: "expense"},
		{Month: "2026-08", Type: "Rent", AmountCents: 125_000, Direction: "expense"},
		{Month: "2026-09", Type: "Rent", AmountCents: 125_000, Direction: "expense"},
	})

	got, err := parseParquet(r, r.Size(), importedAt)
	if err != nil {
		t.Fatalf("parseParquet(): %v", err)
	}

	// June has happened, so it keeps its own month. August and September have
	// not, so both are dated at the import — kept apart, and kept in order, by
	// their position in the file.
	want := []domain.Event{
		{Action: domain.ActionSet, Month: "2026-06", Type: "Rent", Amount: 120_000, Direction: domain.DirectionExpense, RecordedAt: time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC)},
		{Action: domain.ActionSet, Month: "2026-08", Type: "Rent", Amount: 125_000, Direction: domain.DirectionExpense, RecordedAt: importedAt.Add(1 * time.Microsecond)},
		{Action: domain.ActionSet, Month: "2026-09", Type: "Rent", Amount: 125_000, Direction: domain.DirectionExpense, RecordedAt: importedAt.Add(2 * time.Microsecond)},
	}
	assertEvents(t, got, want)

	// The point of all of it: a correction made after the import still outranks it.
	correction := importedAt.Add(time.Hour)
	for _, event := range got {
		if !event.RecordedAt.Before(correction) {
			t.Fatalf("event for %s recorded at %s, want before a correction made at %s", event.Month, event.RecordedAt, correction)
		}
	}
}

func TestParseParquetRejectsAFileThatIsNotParquet(t *testing.T) {
	r := bytes.NewReader([]byte("Month,Rent\n2026-01,$1200.00\n"))

	_, err := parseParquet(r, r.Size(), importedAt)
	assertErrorContains(t, err, "open parquet")
}

// writeParquet writes rows to an in-memory Parquet file. The type parameter is
// what makes the schema tests possible: they hand it a row type that is subtly
// wrong, exactly as a conversion script would.
func writeParquet[T any](t *testing.T, rows []T) *bytes.Reader {
	t.Helper()

	var buf bytes.Buffer
	w := parquet.NewGenericWriter[T](&buf)
	if _, err := w.Write(rows); err != nil {
		t.Fatalf("Write(%d rows): %v", len(rows), err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
	return bytes.NewReader(buf.Bytes())
}

func writeSample(t *testing.T) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(samplePath), 0o755); err != nil {
		t.Fatalf("MkdirAll(testdata): %v", err)
	}

	r := writeParquet(t, sampleRows)
	b := make([]byte, r.Size())
	if _, err := r.ReadAt(b, 0); err != nil {
		t.Fatalf("ReadAt(): %v", err)
	}
	if err := os.WriteFile(samplePath, b, 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", samplePath, err)
	}
	t.Logf("regenerated %s", samplePath)
}

func assertEvents(t *testing.T, got, want []domain.Event) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("parseParquet() = %d events, want %d\ngot:  %+v\nwant: %+v", len(got), len(want), got, want)
	}
	for i := range want {
		if !equal(got[i], want[i]) {
			t.Errorf("event %d =\n\t%+v\nwant\n\t%+v", i, got[i], want[i])
		}
	}
}

// equal compares events the way internal/eventlog's conformance test does:
// field by field, with the timestamp compared as an instant. A time.Time carries
// a location pointer and a monotonic reading that reflect.DeepEqual would
// compare and Equal knows to ignore.
func equal(a, b domain.Event) bool {
	return a.ID == b.ID &&
		a.Action == b.Action &&
		a.Month == b.Month &&
		a.Type == b.Type &&
		a.Amount == b.Amount &&
		a.Direction == b.Direction &&
		a.Note == b.Note &&
		a.RefEventID == b.RefEventID &&
		a.RecordedAt.Equal(b.RecordedAt)
}

func assertErrorContains(t *testing.T, err error, want string) {
	t.Helper()

	if err == nil {
		t.Fatalf("parseParquet() = nil, want an error containing %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("parseParquet() = %q, want it to contain %q", err, want)
	}
}
