package main

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Zaba505/expense-tracker/internal/domain"
	"github.com/Zaba505/expense-tracker/internal/money"
)

func TestParseCSV(t *testing.T) {
	t.Parallel()

	f, err := os.Open("testdata/sample.csv")
	if err != nil {
		t.Fatalf("Open(sample.csv): %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })

	got, err := parseCSV(f)
	if err != nil {
		t.Fatalf("parseCSV(sample.csv): %v", err)
	}

	want := []domain.Event{
		{Action: domain.ActionSet, Month: "2026-01", Type: "Rent", Amount: 120_000, Direction: domain.DirectionExpense, RecordedAt: time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)},
		{Action: domain.ActionSet, Month: "2026-01", Type: "Groceries", Amount: 24_567, Direction: domain.DirectionExpense, RecordedAt: time.Date(2026, time.January, 1, 0, 0, 0, 1_000, time.UTC)},
		{Action: domain.ActionSet, Month: "2026-01", Type: "Monthly Spend", Amount: 7_500, Direction: domain.DirectionExpense, RecordedAt: time.Date(2026, time.January, 1, 0, 0, 0, 2_000, time.UTC)},
		{Action: domain.ActionSet, Month: "2026-01", Type: "Income", Amount: 400_000, Direction: domain.DirectionIncome, RecordedAt: time.Date(2026, time.January, 1, 0, 0, 0, 3_000, time.UTC)},
		{Action: domain.ActionSet, Month: "2026-02", Type: "Rent", Amount: 120_000, Direction: domain.DirectionExpense, RecordedAt: time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC)},
		{Action: domain.ActionSet, Month: "2026-02", Type: "Monthly Spend", Amount: 8_000, Direction: domain.DirectionExpense, RecordedAt: time.Date(2026, time.February, 1, 0, 0, 0, 1_000, time.UTC)},
		{Action: domain.ActionSet, Month: "2026-02", Type: "Income", Amount: 410_000, Direction: domain.DirectionIncome, RecordedAt: time.Date(2026, time.February, 1, 0, 0, 0, 2_000, time.UTC)},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseCSV(sample.csv) =\n%#v\nwant\n%#v", got, want)
	}
}

func TestParseCSV_ReportsCellOnAmountError(t *testing.T) {
	t.Parallel()

	_, err := parseCSV(strings.NewReader("Month,Rent\n2026-01,not money\n"))
	if err == nil {
		t.Fatal("parseCSV() error = nil, want row/column parse error")
	}

	if !errors.Is(err, money.ErrSyntax) {
		t.Fatalf("parseCSV() error = %v, want money.ErrSyntax", err)
	}

	var cellErr *cellError
	if !errors.As(err, &cellErr) {
		t.Fatalf("parseCSV() error = %T, want *cellError", err)
	}

	if cellErr.Row != 2 || cellErr.Column != 2 || cellErr.Header != "Rent" {
		t.Fatalf("cellError = %+v, want row 2 column 2 header Rent", cellErr)
	}
}
