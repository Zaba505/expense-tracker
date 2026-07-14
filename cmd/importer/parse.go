package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Zaba505/expense-tracker/internal/domain"
	"github.com/Zaba505/expense-tracker/internal/money"
)

var ignoredColumns = map[string]struct{}{
	"expenses":            {},
	"monthly bills total": {},
	"net":                 {},
	"savings":             {},
	"total expenditure":   {},
}

var monthLayouts = []string{
	"2006-01",
	"2006-01-02",
	"Jan 2006",
	"January 2006",
}

// cellError reports which CSV cell could not be translated.
type cellError struct {
	Row    int
	Column int
	Header string
	Err    error
}

func (e *cellError) Error() string {
	if e.Header == "" {
		return fmt.Sprintf("importer: row %d column %d: %v", e.Row, e.Column, e.Err)
	}
	return fmt.Sprintf("importer: row %d column %d (%s): %v", e.Row, e.Column, e.Header, e.Err)
}

func (e *cellError) Unwrap() error { return e.Err }

// parseCSV translates the exported sheet CSV into the import event stream.
func parseCSV(r io.Reader) ([]domain.Event, error) {
	rows := csv.NewReader(r)
	rows.FieldsPerRecord = -1
	rows.TrimLeadingSpace = true

	header, err := rows.Read()
	if err != nil {
		if err == io.EOF {
			return nil, fmt.Errorf("importer: empty csv")
		}
		return nil, fmt.Errorf("importer: read header: %w", err)
	}
	for i := range header {
		header[i] = strings.TrimSpace(header[i])
	}

	monthColumn := -1
	for i, name := range header {
		if strings.EqualFold(name, "Month") {
			monthColumn = i
			break
		}
	}
	if monthColumn < 0 {
		return nil, fmt.Errorf("importer: missing Month column")
	}

	monthEventCounts := make(map[string]int)
	var events []domain.Event
	for rowNumber := 2; ; rowNumber++ {
		record, err := rows.Read()
		if err != nil {
			if err == io.EOF {
				return events, nil
			}
			return nil, fmt.Errorf("importer: read row %d: %w", rowNumber, err)
		}
		if isBlankRecord(record) {
			continue
		}

		month, recordedAt, err := parseMonth(safeGetField(record, monthColumn))
		if err != nil {
			return nil, &cellError{
				Row:    rowNumber,
				Column: monthColumn + 1,
				Header: header[monthColumn],
				Err:    err,
			}
		}

		for columnNumber, name := range header {
			if columnNumber == monthColumn || ignoredColumn(name) || name == "" {
				continue
			}

			value := strings.TrimSpace(safeGetField(record, columnNumber))
			if value == "" {
				continue
			}

			amount, err := money.Parse(value)
			if err != nil {
				return nil, &cellError{
					Row:    rowNumber,
					Column: columnNumber + 1,
					Header: name,
					Err:    err,
				}
			}

			sequence := monthEventCounts[month]
			monthEventCounts[month] = sequence + 1

			direction := domain.DirectionExpense
			if strings.EqualFold(name, "Income") {
				direction = domain.DirectionIncome
			}

			events = append(events, domain.Event{
				Action:     domain.ActionSet,
				Month:      month,
				Type:       name,
				Amount:     amount,
				Direction:  direction,
				RecordedAt: recordedAt.Add(time.Duration(sequence) * time.Microsecond),
			})
		}
	}
}

func ignoredColumn(name string) bool {
	_, ignored := ignoredColumns[strings.ToLower(strings.TrimSpace(name))]
	return ignored
}

func parseMonth(value string) (string, time.Time, error) {
	value = strings.TrimSpace(value)
	for _, layout := range monthLayouts {
		t, err := time.Parse(layout, value)
		if err != nil {
			continue
		}
		month := domain.Month(t)
		return month, time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC), nil
	}
	return "", time.Time{}, fmt.Errorf("invalid month %q", value)
}

func safeGetField(record []string, index int) string {
	if index >= len(record) {
		return ""
	}
	return record[index]
}

func isBlankRecord(record []string) bool {
	for _, field := range record {
		if strings.TrimSpace(field) != "" {
			return false
		}
	}
	return true
}
