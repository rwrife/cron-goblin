package gaps

import (
	"testing"
	"time"
)

// mustUTC is a fixed, DST-free reference start so golden expectations are
// stable. Sunday 2026-01-04 00:00:00 UTC.
func refStart() time.Time {
	return time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC)
}

func TestAnalyze_GoldenGaps(t *testing.T) {
	// Two daily jobs at 02:00 and 05:00 over a single day. The quiet windows
	// (longest first) should be: 05:01→next-day-00:00-ish, 00:00→02:00,
	// 02:01→05:00. We use a 1-day window to keep it tight.
	src := "0 2 * * *\n0 5 * * *\n"
	rep, err := Analyze(src, refStart(), 1, 5, time.UTC)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if rep.Analyzed != 2 {
		t.Fatalf("Analyzed = %d, want 2", rep.Analyzed)
	}
	if rep.Skipped != 0 {
		t.Fatalf("Skipped = %d, want 0", rep.Skipped)
	}
	// Longest gap is from 05:01 to the window end (24h). Its start must be 05:01.
	longest := rep.Gaps[0]
	wantStart := time.Date(2026, 1, 4, 5, 1, 0, 0, time.UTC)
	if !longest.Start.Equal(wantStart) {
		t.Errorf("longest gap start = %v, want %v", longest.Start, wantStart)
	}
	// The gap between 02:00 and 05:00 is 02:01 → 05:00 = 2h59m.
	var mid *Gap
	for i := range rep.Gaps {
		if rep.Gaps[i].Start.Equal(time.Date(2026, 1, 4, 2, 1, 0, 0, time.UTC)) {
			mid = &rep.Gaps[i]
		}
	}
	if mid == nil {
		t.Fatalf("expected a gap starting 02:01; gaps=%v", rep.Gaps)
	}
	if mid.Duration != 2*time.Hour+59*time.Minute {
		t.Errorf("mid gap duration = %v, want 2h59m", mid.Duration)
	}
	// Gaps must be sorted longest first.
	for i := 1; i < len(rep.Gaps); i++ {
		if rep.Gaps[i-1].Duration < rep.Gaps[i].Duration {
			t.Errorf("gaps not sorted longest-first at %d", i)
		}
	}
}

func TestAnalyze_WindowClamping(t *testing.T) {
	// A single daily job at noon; a 1-day window starting at midnight. The
	// leading gap must clamp to the window start (00:00), and the trailing gap
	// must clamp to the window end (next midnight), never beyond.
	src := "0 12 * * *\n"
	from := refStart()
	rep, err := Analyze(src, from, 1, 10, time.UTC)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	to := from.Add(24 * time.Hour)
	for _, g := range rep.Gaps {
		if g.Start.Before(from) {
			t.Errorf("gap start %v before window start %v", g.Start, from)
		}
		if g.End.After(to) {
			t.Errorf("gap end %v after window end %v", g.End, to)
		}
	}
	// Leading gap should start exactly at the window opening.
	if !rep.Gaps[len(rep.Gaps)-1].Start.Equal(from) && !rep.Gaps[0].Start.Equal(from) {
		found := false
		for _, g := range rep.Gaps {
			if g.Start.Equal(from) {
				found = true
			}
		}
		if !found {
			t.Errorf("no gap starts at window open %v; gaps=%v", from, rep.Gaps)
		}
	}
}

func TestAnalyze_EveryMinuteNoGaps(t *testing.T) {
	rep, err := Analyze("* * * * *\n", refStart(), 1, 5, time.UTC)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(rep.Gaps) != 0 {
		t.Errorf("every-minute job should yield 0 gaps, got %d: %v", len(rep.Gaps), rep.Gaps)
	}
}

func TestAnalyze_BusiestMinuteTieBreak(t *testing.T) {
	// Two jobs fire at 03:00 (count 2) and two others at 21:00 (count 2). The
	// tie must break to the earlier minute deterministically: 03:00.
	src := "0 3 * * *\n0 3 * * *\n0 21 * * *\n0 21 * * *\n"
	rep, err := Analyze(src, refStart(), 1, 5, time.UTC)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if rep.Busiest.Count != 2 {
		t.Fatalf("busiest count = %d, want 2", rep.Busiest.Count)
	}
	want := time.Date(2026, 1, 4, 3, 0, 0, 0, time.UTC)
	if !rep.Busiest.Time.Equal(want) {
		t.Errorf("busiest tie broke to %v, want earliest %v", rep.Busiest.Time, want)
	}
}

func TestAnalyze_EmptyCrontab(t *testing.T) {
	rep, err := Analyze("\n# only a comment\n", refStart(), 3, 5, time.UTC)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(rep.Gaps) != 1 {
		t.Fatalf("empty crontab should be one whole-window gap, got %d", len(rep.Gaps))
	}
	if rep.Gaps[0].Duration != 3*24*time.Hour {
		t.Errorf("whole-window gap = %v, want 72h", rep.Gaps[0].Duration)
	}
	if rep.Busiest.Count != 0 {
		t.Errorf("busiest count = %d, want 0 for empty crontab", rep.Busiest.Count)
	}
}

func TestAnalyze_SkipsDeadAndUnparseable(t *testing.T) {
	// A dead expression (Feb 30) and a garbage line are both ignored for gap
	// math but counted as skipped; the one live daily job is analyzed.
	src := "0 6 * * *\n0 0 30 2 *\ntotally not cron\n"
	rep, err := Analyze(src, refStart(), 1, 5, time.UTC)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if rep.Analyzed != 1 {
		t.Errorf("Analyzed = %d, want 1", rep.Analyzed)
	}
	if rep.Skipped != 2 {
		t.Errorf("Skipped = %d, want 2", rep.Skipped)
	}
}

func TestAnalyze_TopCaps(t *testing.T) {
	// Many sparse jobs create many gaps; --top must cap the result count while
	// still returning the longest ones.
	src := "0 1 * * *\n0 5 * * *\n0 9 * * *\n0 13 * * *\n0 17 * * *\n0 21 * * *\n"
	rep, err := Analyze(src, refStart(), 1, 3, time.UTC)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(rep.Gaps) != 3 {
		t.Fatalf("top=3 should cap to 3 gaps, got %d", len(rep.Gaps))
	}
	// top<=0 returns all.
	all, _ := Analyze(src, refStart(), 1, 0, time.UTC)
	if len(all.Gaps) <= 3 {
		t.Errorf("top<=0 should return all gaps, got %d", len(all.Gaps))
	}
}

func TestAnalyze_Defaults(t *testing.T) {
	rep, err := Analyze("0 12 * * *\n", refStart(), 0, 0, time.UTC)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if rep.Window != DefaultDays*24*time.Hour {
		t.Errorf("default window = %v, want %d days", rep.Window, DefaultDays)
	}
}
