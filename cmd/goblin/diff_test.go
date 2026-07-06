package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// runDiff builds a fresh diff command and captures its streams.
func runDiff(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := newDiffCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

// countMarkers tallies the leading +/-/= markers in the human timeline.
func countMarkers(stdout string) (added, removed, same int) {
	for _, ln := range strings.Split(stdout, "\n") {
		trimmed := strings.TrimSpace(ln)
		// Only count timeline rows: "<marker> <RFC3339>". The header lines that
		// start with "- <expr>" / "+ <expr>" carry no 'T'-dated timestamp.
		if !strings.Contains(trimmed, "T") {
			continue
		}
		switch {
		case strings.HasPrefix(trimmed, "+ "):
			added++
		case strings.HasPrefix(trimmed, "- "):
			removed++
		case strings.HasPrefix(trimmed, "= "):
			same++
		}
	}
	return
}

func TestDiffShiftedSchedule(t *testing.T) {
	// Shifting 09:00 -> 09:30 daily: every compared run is removed on the old
	// side and re-added on the new side; nothing stays put.
	stdout, _, err := runDiff(t, "--quiet", "--tz", "UTC", "-n", "4", "0 9 * * *", "30 9 * * *")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	added, removed, same := countMarkers(stdout)
	if added != 4 || removed != 4 || same != 0 {
		t.Errorf("markers = (+%d -%d =%d), want (+4 -4 =0)\n%s", added, removed, same, stdout)
	}
	if !strings.Contains(stdout, "4 added, 4 removed, 0 unchanged.") {
		t.Errorf("summary line missing/incorrect:\n%s", stdout)
	}
}

func TestDiffIdenticalIsNoOp(t *testing.T) {
	stdout, _, err := runDiff(t, "--quiet", "--tz", "UTC", "-n", "3", "0 9 * * *", "0 9 * * *")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	added, removed, same := countMarkers(stdout)
	if added != 0 || removed != 0 || same != 3 {
		t.Errorf("markers = (+%d -%d =%d), want (+0 -0 =3)\n%s", added, removed, same, stdout)
	}
	if !strings.Contains(stdout, "no-op") {
		t.Errorf("expected a no-op note for identical schedules:\n%s", stdout)
	}
}

func TestDiffNarrowingNeverAddsAndDropsWeekends(t *testing.T) {
	// Narrowing "every day" -> "weekdays" makes the new schedule a strict
	// subset of the old one. Two invariants hold no matter what time or day the
	// test runs:
	//   1. Nothing is ever ADDED (a subset can't introduce new runs).
	//   2. Every UNCHANGED run is a weekday, and every REMOVED run is a weekend
	//      day (the only runs the filter drops).
	// Asserting the invariants rather than fixed counts keeps this independent
	// of the wall clock (diff uses time.Now()).
	stdout, _, err := runDiff(t, "--quiet", "--tz", "UTC", "--window", "14d", "0 12 * * *", "0 12 * * 1-5")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	added, removed, same := countMarkers(stdout)
	if added != 0 {
		t.Errorf("narrowing to a subset should add nothing, got +%d\n%s", added, stdout)
	}
	if same == 0 || removed == 0 {
		t.Errorf("a 14-day window should contain both kept (weekday) and dropped (weekend) runs; got =%d -%d\n%s", same, removed, stdout)
	}
	// Verify each timeline row lands on the weekday/weekend it claims.
	for _, ln := range strings.Split(stdout, "\n") {
		trimmed := strings.TrimSpace(ln)
		if !strings.Contains(trimmed, "T12:00:00") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) < 2 {
			continue
		}
		marker, iso := fields[0], fields[1]
		ts, perr := time.Parse(time.RFC3339, iso)
		if perr != nil {
			continue
		}
		weekend := ts.Weekday() == time.Saturday || ts.Weekday() == time.Sunday
		switch marker {
		case "=":
			if weekend {
				t.Errorf("unchanged run fell on a weekend (%s): %s", ts.Weekday(), iso)
			}
		case "-":
			if !weekend {
				t.Errorf("removed run was not a weekend day (%s): %s", ts.Weekday(), iso)
			}
		case "+":
			t.Errorf("unexpected added run: %s", iso)
		}
	}
}

func TestDiffJSONShape(t *testing.T) {
	stdout, _, err := runDiff(t, "--quiet", "--json", "--tz", "UTC", "-n", "4", "0 9 * * *", "30 9 * * *")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	var payload struct {
		Old        string   `json:"old"`
		New        string   `json:"new"`
		OldEnglish string   `json:"old_english"`
		NewEnglish string   `json:"new_english"`
		Timezone   string   `json:"timezone"`
		Window     string   `json:"window"`
		Added      []string `json:"added"`
		Removed    []string `json:"removed"`
		Unchanged  []string `json:"unchanged"`
		Timeline   []struct {
			Time string `json:"time"`
			Kind string `json:"kind"`
		} `json:"timeline"`
		Summary struct {
			Added     int  `json:"added"`
			Removed   int  `json:"removed"`
			Unchanged int  `json:"unchanged"`
			Identical bool `json:"identical"`
		} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, stdout)
	}
	if payload.Old != "0 9 * * *" || payload.New != "30 9 * * *" {
		t.Errorf("expressions round-tripped wrong: old=%q new=%q", payload.Old, payload.New)
	}
	if payload.Timezone != "UTC" {
		t.Errorf("timezone = %q, want UTC", payload.Timezone)
	}
	if len(payload.Added) != 4 || len(payload.Removed) != 4 || len(payload.Unchanged) != 0 {
		t.Errorf("bucket sizes: added=%d removed=%d unchanged=%d, want 4/4/0",
			len(payload.Added), len(payload.Removed), len(payload.Unchanged))
	}
	if payload.Summary.Added != 4 || payload.Summary.Removed != 4 || payload.Summary.Identical {
		t.Errorf("summary mismatch: %+v", payload.Summary)
	}
	// Timeline must be the union (8 entries) and strictly chronological.
	if len(payload.Timeline) != 8 {
		t.Fatalf("timeline should hold the 8-entry union, got %d", len(payload.Timeline))
	}
	var prev time.Time
	for i, e := range payload.Timeline {
		ts, perr := time.Parse(time.RFC3339, e.Time)
		if perr != nil {
			t.Fatalf("timeline[%d].time not RFC3339: %q", i, e.Time)
		}
		if i > 0 && ts.Before(prev) {
			t.Errorf("timeline not sorted at %d: %s before %s", i, e.Time, prev.Format(time.RFC3339))
		}
		prev = ts
		if e.Kind != "added" && e.Kind != "removed" {
			t.Errorf("timeline[%d].kind = %q, want added/removed for a pure shift", i, e.Kind)
		}
	}
}

func TestDiffJSONIdentical(t *testing.T) {
	stdout, _, err := runDiff(t, "--quiet", "--json", "--tz", "UTC", "-n", "3", "0 0 * * *", "0 0 * * *")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	var payload struct {
		Summary struct {
			Identical bool `json:"identical"`
			Unchanged int  `json:"unchanged"`
		} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, stdout)
	}
	if !payload.Summary.Identical {
		t.Error("summary.identical should be true for two equal schedules")
	}
	if payload.Summary.Unchanged != 3 {
		t.Errorf("unchanged = %d, want 3", payload.Summary.Unchanged)
	}
}

func TestDiffRejectsBadExpression(t *testing.T) {
	_, stderr, err := runDiff(t, "--quiet", "--tz", "UTC", "0 9 * * *", "99 9 * * *")
	if err == nil {
		t.Fatal("expected an error for an out-of-range new expression")
	}
	if !strings.Contains(stderr, "new expression") {
		t.Errorf("stderr should point at the new expression, got: %q", stderr)
	}
}

func TestDiffRejectsBadOldExpression(t *testing.T) {
	_, stderr, err := runDiff(t, "--quiet", "--tz", "UTC", "not-a-cron", "0 9 * * *")
	if err == nil {
		t.Fatal("expected an error for a malformed old expression")
	}
	if !strings.Contains(stderr, "old expression") {
		t.Errorf("stderr should point at the old expression, got: %q", stderr)
	}
}

func TestDiffWindowAndCountAreExclusive(t *testing.T) {
	_, stderr, err := runDiff(t, "--quiet", "--tz", "UTC", "--window", "7d", "-n", "5", "0 9 * * *", "0 8 * * *")
	if err == nil {
		t.Fatal("expected an error when both --window and -n are set")
	}
	if !strings.Contains(stderr, "mutually exclusive") {
		t.Errorf("stderr should explain the exclusivity, got: %q", stderr)
	}
}

func TestDiffRejectsBadWindow(t *testing.T) {
	_, stderr, err := runDiff(t, "--quiet", "--tz", "UTC", "--window", "banana", "0 9 * * *", "0 8 * * *")
	if err == nil {
		t.Fatal("expected an error for an unparseable window")
	}
	if !strings.Contains(stderr, "window") {
		t.Errorf("stderr should mention the window, got: %q", stderr)
	}
}

func TestDiffPersonaOnStderrByDefault(t *testing.T) {
	// Without --quiet the goblin grumbles on stderr; stdout stays clean facts.
	stdout, stderr, err := runDiff(t, "--tz", "UTC", "-n", "2", "0 9 * * *", "0 10 * * *")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if strings.TrimSpace(stderr) == "" {
		t.Error("expected goblin persona on stderr without --quiet")
	}
	if !strings.Contains(stdout, "added,") {
		t.Errorf("stdout should still carry the summary, got: %q", stdout)
	}
}

// TestParseWindow exercises the friendly duration parser directly, including
// the 'd' (day) extension stdlib lacks.
func TestParseWindow(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"7d", 7 * 24 * time.Hour, false},
		{"1d", 24 * time.Hour, false},
		{"48h", 48 * time.Hour, false},
		{"90m", 90 * time.Minute, false},
		{"36h30m", 36*time.Hour + 30*time.Minute, false},
		{"0h", 0, true},
		{"-3h", 0, true},
		{"banana", 0, true},
		{"", 0, true},
		{"d", 0, true},
	}
	for _, c := range cases {
		got, err := parseWindow(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseWindow(%q) = %v, want error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseWindow(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseWindow(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestDiffRejectsZeroCount(t *testing.T) {
	_, stderr, err := runDiff(t, "--quiet", "--tz", "UTC", "-n", "0", "0 9 * * *", "0 8 * * *")
	if err == nil {
		t.Fatal("expected an error for -n 0")
	}
	if !strings.Contains(stderr, "must be positive") {
		t.Errorf("stderr should reject a non-positive count, got: %q", stderr)
	}
}
