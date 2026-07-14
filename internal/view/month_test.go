package view

import (
	"testing"
	"time"
)

func TestRecordedAtDisplayUsesUTC(t *testing.T) {
	t.Parallel()

	recordedAt := time.Date(2026, time.July, 14, 8, 30, 45, 0, time.FixedZone("PDT", -7*60*60))

	if got, want := RecordedAtDisplay(recordedAt), "2026-07-14 15:30:45 UTC"; got != want {
		t.Fatalf("RecordedAtDisplay(%v) = %q, want %q", recordedAt, got, want)
	}
}
