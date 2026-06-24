package render

import (
	"strings"
	"testing"
	"time"
)

// mustUTC builds a UTC time for table-driven tests.
func mustUTC(y int, mo time.Month, d, h, mi int) time.Time {
	return time.Date(y, mo, d, h, mi, 0, 0, time.UTC)
}

func TestBuildHeatGrid_BucketsByWeekdayAndHour(t *testing.T) {
	// 2026-06-22 is a Monday. Two fires at 09:xx Monday, one at 14:00 Tuesday.
	times := []time.Time{
		mustUTC(2026, 6, 22, 9, 0),  // Mon 09
		mustUTC(2026, 6, 22, 9, 30), // Mon 09 (same cell)
		mustUTC(2026, 6, 23, 14, 0), // Tue 14
	}
	g := BuildHeatGrid(times, time.UTC)

	if got := g[time.Monday][9]; got != 2 {
		t.Errorf("Monday 09 count = %d, want 2", got)
	}
	if got := g[time.Tuesday][14]; got != 1 {
		t.Errorf("Tuesday 14 count = %d, want 1", got)
	}
	if got := g.Total(); got != 3 {
		t.Errorf("Total = %d, want 3", got)
	}
	if got := g.Max(); got != 2 {
		t.Errorf("Max = %d, want 2", got)
	}
}

func TestBuildHeatGrid_RespectsLocation(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	// 2026-06-22 04:00 UTC is 2026-06-22 00:00 EDT (Monday). In UTC it's still
	// Monday 04; the cell differs by zone, proving location is honored.
	utcTime := mustUTC(2026, 6, 22, 4, 0)

	gUTC := BuildHeatGrid([]time.Time{utcTime}, time.UTC)
	gNY := BuildHeatGrid([]time.Time{utcTime}, ny)

	if gUTC[time.Monday][4] != 1 {
		t.Errorf("UTC grid should have Monday 04 = 1, got %d", gUTC[time.Monday][4])
	}
	if gNY[time.Monday][0] != 1 {
		t.Errorf("NY grid should have Monday 00 = 1, got %d", gNY[time.Monday][0])
	}
}

func TestGlyphFor(t *testing.T) {
	// Zero count always the empty glyph.
	if g := glyphFor(0, 5); g != heatRamp[0] {
		t.Errorf("glyphFor(0,5) = %q, want empty glyph %q", g, heatRamp[0])
	}
	// The max-count cell uses the densest glyph.
	if g := glyphFor(5, 5); g != heatRamp[len(heatRamp)-1] {
		t.Errorf("glyphFor(5,5) = %q, want densest glyph %q", g, heatRamp[len(heatRamp)-1])
	}
	// A low positive count never maps to empty.
	if g := glyphFor(1, 10); g == heatRamp[0] {
		t.Errorf("glyphFor(1,10) should not be the empty glyph")
	}
}

func TestHeatmap_EmptyGridExplains(t *testing.T) {
	var g HeatGrid
	out := Heatmap(g, NoColorPalette())
	if !strings.Contains(out, "no fires") {
		t.Errorf("empty heatmap should explain itself, got %q", out)
	}
}

func TestHeatmap_RendersAllWeekdayRows(t *testing.T) {
	times := []time.Time{mustUTC(2026, 6, 22, 9, 0)}
	g := BuildHeatGrid(times, time.UTC)
	out := Heatmap(g, NoColorPalette())
	for _, day := range dowShort {
		if !strings.Contains(out, day) {
			t.Errorf("heatmap missing weekday row %q\n%s", day, out)
		}
	}
	if !strings.Contains(out, "peak 1") {
		t.Errorf("heatmap should report peak count, got:\n%s", out)
	}
}

func TestNextRuns_EmptyIsNeverFires(t *testing.T) {
	out := NextRuns(nil, time.Now(), time.UTC, 5, NoColorPalette())
	if !strings.Contains(out, "never fires") {
		t.Errorf("empty runs should say never fires, got %q", out)
	}
}

func TestNextRuns_FormatsAndLimits(t *testing.T) {
	now := mustUTC(2026, 6, 22, 8, 0)
	runs := []time.Time{
		mustUTC(2026, 6, 22, 9, 0),
		mustUTC(2026, 6, 22, 10, 0),
		mustUTC(2026, 6, 22, 11, 0),
	}
	out := NextRuns(runs, now, time.UTC, 2, NoColorPalette())
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("limit=2 should yield 2 lines, got %d:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[0], "2026-06-22 09:00") {
		t.Errorf("first run line wrong: %q", lines[0])
	}
	// 09:00 is one hour after 08:00 now.
	if !strings.Contains(lines[0], "in 1h") {
		t.Errorf("expected relative hint 'in 1h', got %q", lines[0])
	}
}

func TestHumanizeUntil(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "now"},
		{-time.Hour, "now"},
		{30 * time.Minute, "in 30m"},
		{90 * time.Minute, "in 1h 30m"},
		{25 * time.Hour, "in 1d 1h"},
	}
	for _, c := range cases {
		if got := humanizeUntil(c.d); got != c.want {
			t.Errorf("humanizeUntil(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestWarnings_SortsAndBullets(t *testing.T) {
	out := Warnings([]string{"zebra", "apple"}, NoColorPalette())
	if out == "" {
		t.Fatal("expected non-empty warnings block")
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 warning lines, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "apple") || !strings.Contains(lines[1], "zebra") {
		t.Errorf("warnings not sorted: %v", lines)
	}
	if Warnings(nil, NoColorPalette()) != "" {
		t.Error("nil warnings should render empty")
	}
}
