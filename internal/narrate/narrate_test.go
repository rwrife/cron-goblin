package narrate

import (
	"strings"
	"testing"
	"time"

	"github.com/rwrife/cron-goblin/internal/parse"
)

func mustParse(t *testing.T, expr string) parse.Schedule {
	t.Helper()
	s, err := parse.Parse(expr)
	if err != nil {
		t.Fatalf("parse(%q): %v", expr, err)
	}
	return s
}

func TestNarrateSingle(t *testing.T) {
	cases := []struct {
		expr string
		want string
	}{
		{"0 9 * * *", "This job runs at 09:00 every day."},
		{"*/15 * * * *", "This job runs every 15 minutes every day."},
		{"30 18 * * 1-5", "This job runs at 18:30 on weekdays (Monday through Friday)."},
	}
	for _, c := range cases {
		got := Narrate(mustParse(t, c.expr))
		if got != c.want {
			t.Errorf("Narrate(%q) = %q, want %q", c.expr, got, c.want)
		}
	}
}

func TestNarrateSingleShape(t *testing.T) {
	got := Narrate(mustParse(t, "0 0 1 1 *"))
	if !strings.HasPrefix(got, "This job runs ") {
		t.Errorf("expected changelog prefix, got %q", got)
	}
	if !strings.HasSuffix(got, ".") {
		t.Errorf("expected trailing period, got %q", got)
	}
}

func TestNarrateChangeMentionsBothClauses(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	old := mustParse(t, "0 * * * *")      // hourly
	newS := mustParse(t, "30 18 * * 1-5") // weekday evening
	got := NarrateChange(old, newS, from, time.UTC)

	if !strings.Contains(got, "18:30") {
		t.Errorf("expected new time in narration, got %q", got)
	}
	if !strings.Contains(got, "instead of") {
		t.Errorf("expected 'instead of' contrast, got %q", got)
	}
	if !strings.Contains(got, "less often") {
		t.Errorf("expected cadence delta (less often), got %q", got)
	}
	if !strings.HasSuffix(got, ".") {
		t.Errorf("expected trailing period, got %q", got)
	}
}

func TestNarrateChangeMoreOften(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	old := mustParse(t, "0 9 * * *")    // once a day
	newS := mustParse(t, "0 */2 * * *") // every 2 hours
	got := NarrateChange(old, newS, from, time.UTC)
	if !strings.Contains(got, "more often") {
		t.Errorf("expected 'more often', got %q", got)
	}
}

func TestNarrateChangeIdentical(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := mustParse(t, "0 9 * * *")
	got := NarrateChange(s, s, from, time.UTC)
	if !strings.Contains(got, "unchanged") {
		t.Errorf("expected 'unchanged' for identical schedules, got %q", got)
	}
}

func TestNarrateChangeNextRunShift(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	old := mustParse(t, "0 9 * * *")
	newS := mustParse(t, "0 8 * * *")
	got := NarrateChange(old, newS, from, time.UTC)
	if !strings.Contains(got, "next run moves from") {
		t.Errorf("expected next-run shift phrasing, got %q", got)
	}
}

func TestRatioPhraseTwice(t *testing.T) {
	if got := ratioPhrase(10, 20); got != "twice" {
		t.Errorf("ratioPhrase(10,20) = %q, want %q", got, "twice")
	}
}

func TestCountInWindow(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// Once daily over 30 days ~= 30 fires.
	n := countInWindow(mustParse(t, "0 9 * * *"), from, 30*24*time.Hour, time.UTC)
	if n < 29 || n > 31 {
		t.Errorf("expected ~30 daily fires in 30d window, got %d", n)
	}
}
