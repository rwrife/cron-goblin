package tui

import (
	"strings"
	"testing"
	"time"
)

// ref is a fixed reference instant used across state tests so next-run output
// is deterministic. 2026-06-22 08:00 UTC is a Monday morning.
func ref() time.Time { return time.Date(2026, 6, 22, 8, 0, 0, 0, time.UTC) }

func TestComputePreview_BlankIsNeitherValidNorError(t *testing.T) {
	for _, in := range []string{"", "   ", "\t"} {
		pv := computePreview(in, ref(), time.UTC)
		if pv.Valid {
			t.Errorf("blank %q should not be valid", in)
		}
		if pv.Err != nil {
			t.Errorf("blank %q should not carry an error, got %v", in, pv.Err)
		}
		if len(pv.Runs) != 0 {
			t.Errorf("blank %q should have no runs", in)
		}
	}
}

func TestComputePreview_InvalidSetsErr(t *testing.T) {
	pv := computePreview("not a cron", ref(), time.UTC)
	if pv.Valid {
		t.Fatal("garbage should not be valid")
	}
	if pv.Err == nil {
		t.Fatal("invalid expression should set Err")
	}
	if pv.English != "" {
		t.Errorf("invalid expression should have no English, got %q", pv.English)
	}
}

func TestComputePreview_ValidPopulatesEverything(t *testing.T) {
	// Every weekday at 09:00.
	pv := computePreview("0 9 * * 1-5", ref(), time.UTC)
	if !pv.Valid {
		t.Fatalf("expected valid, got Err=%v", pv.Err)
	}
	if pv.Never {
		t.Fatal("0 9 * * 1-5 should fire")
	}
	if len(pv.Runs) == 0 {
		t.Fatal("expected upcoming runs")
	}
	if !strings.Contains(strings.ToLower(pv.English), "weekday") {
		t.Errorf("English should mention weekdays, got %q", pv.English)
	}
	// First run should be Monday 2026-06-22 09:00 (one hour after ref).
	first := pv.Runs[0]
	if first.Hour() != 9 || first.Weekday() != time.Monday {
		t.Errorf("first run = %v, want Monday 09:00", first)
	}
	// Heatmap should reflect the same number of fires as Runs.
	if pv.Grid.Total() != len(pv.Runs) {
		t.Errorf("grid total %d != runs %d", pv.Grid.Total(), len(pv.Runs))
	}
}

func TestComputePreview_NeverFiresFlag(t *testing.T) {
	// February 30th: never fires.
	pv := computePreview("0 0 30 2 *", ref(), time.UTC)
	if !pv.Valid {
		t.Fatalf("expression parses fine; Err=%v", pv.Err)
	}
	if !pv.Never {
		t.Error("Feb 30 should be flagged as never firing")
	}
	if len(pv.Runs) != 0 {
		t.Errorf("never-firing schedule should have no runs, got %d", len(pv.Runs))
	}
}

func TestComputePreview_EveryMinuteWarns(t *testing.T) {
	pv := computePreview("* * * * *", ref(), time.UTC)
	if !pv.Valid {
		t.Fatalf("`* * * * *` should be valid, Err=%v", pv.Err)
	}
	if len(pv.Warnings) == 0 {
		t.Error("every-minute schedule should surface a too-frequent warning")
	}
}
