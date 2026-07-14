package web

import (
	"testing"
)

func TestPreviousYear(t *testing.T) {
	t.Parallel()

	if got := previousYear("2026"); got != "2025" {
		t.Fatalf("previousYear(2026) = %q, want %q", got, "2025")
	}
}

func TestNextYear(t *testing.T) {
	t.Parallel()

	if got := nextYear("2026"); got != "2027" {
		t.Fatalf("nextYear(2026) = %q, want %q", got, "2027")
	}
}

func TestPreviousYear_PanicsOnInvalidYear(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("previousYear did not panic on an invalid year")
		}
	}()

	previousYear("not-a-year")
}
