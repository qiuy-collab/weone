package proactive

import (
	"testing"
	"time"
)

func TestParseCronStandardAcceptsFiveFieldExpressions(t *testing.T) {
	tests := []string{
		"*/5 * * * *",
		"0 9 * * 1-5",
		"15 8 1 * *",
	}
	for _, spec := range tests {
		spec := spec
		t.Run(spec, func(t *testing.T) {
			schedule, err := parseCronStandard(spec)
			if err != nil {
				t.Fatalf("parseCronStandard(%q) error = %v", spec, err)
			}
			if schedule == nil {
				t.Fatalf("parseCronStandard(%q) returned nil schedule", spec)
			}
		})
	}
}

func TestParseCronStandardAcceptsSixFieldExpressionsByDroppingSeconds(t *testing.T) {
	schedule, err := parseCronStandard("0 0 8 * * *")
	if err != nil {
		t.Fatalf("parseCronStandard returned error: %v", err)
	}
	loc := time.UTC
	after := time.Date(2026, 5, 1, 7, 30, 0, 0, loc)
	next := nextCronRun(schedule, after, loc)
	want := time.Date(2026, 5, 1, 8, 0, 0, 0, loc)
	if !next.Equal(want) {
		t.Fatalf("nextCronRun = %s, want %s", next.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

func TestParseCronStandardRejectsInvalidExpressions(t *testing.T) {
	tests := []string{
		"",
		"* * * *",
		"60 * * * *",
		"0 9 * * 8",
	}
	for _, spec := range tests {
		spec := spec
		t.Run(spec, func(t *testing.T) {
			if _, err := parseCronStandard(spec); err == nil {
				t.Fatalf("parseCronStandard(%q) unexpectedly succeeded", spec)
			}
		})
	}
}

func TestParseCronStandardNormalizesSundaySeven(t *testing.T) {
	schedule, err := parseCronStandard("0 9 * * 7")
	if err != nil {
		t.Fatalf("parseCronStandard returned error: %v", err)
	}
	loc := time.FixedZone("UTC+8", 8*60*60)
	after := time.Date(2026, 5, 1, 10, 0, 0, 0, loc)
	next := nextCronRun(schedule, after, loc)
	want := time.Date(2026, 5, 3, 9, 0, 0, 0, loc)
	if !next.Equal(want) {
		t.Fatalf("nextCronRun = %s, want %s", next.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

func TestNextCronRunUsesNamedTimezone(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("LoadLocation error: %v", err)
	}
	schedule, err := parseCronStandard("0 9 * * 1-5")
	if err != nil {
		t.Fatalf("parseCronStandard returned error: %v", err)
	}
	after := time.Date(2026, 5, 1, 8, 30, 0, 0, loc)
	next := nextCronRun(schedule, after, loc)
	want := time.Date(2026, 5, 1, 9, 0, 0, 0, loc)
	if !next.Equal(want) {
		t.Fatalf("nextCronRun = %s, want %s", next.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

func TestNextCronRunAdvancesFromCurrentMinute(t *testing.T) {
	loc := time.UTC
	schedule, err := parseCronStandard("*/5 * * * *")
	if err != nil {
		t.Fatalf("parseCronStandard returned error: %v", err)
	}
	after := time.Date(2026, 5, 1, 9, 10, 0, 0, loc)
	next := nextCronRun(schedule, after, loc)
	want := time.Date(2026, 5, 1, 9, 15, 0, 0, loc)
	if !next.Equal(want) {
		t.Fatalf("nextCronRun = %s, want %s", next.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}
