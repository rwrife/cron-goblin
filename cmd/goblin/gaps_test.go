package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// runGaps builds a fresh gaps command, optionally feeds stdin, and captures its
// streams. Mirrors runStagger in stagger_test.go.
func runGaps(t *testing.T, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := newGapsCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	if stdin != "" {
		cmd.SetIn(strings.NewReader(stdin))
	}
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

func TestGapsHumanReportsQuietWindows(t *testing.T) {
	stdin := "0 2 * * *\n0 5 * * *\n"
	out, _, err := runGaps(t, stdin, "--tz", "UTC", "--days", "1", "--quiet", "-")
	if err != nil {
		t.Fatalf("gaps: %v", err)
	}
	if !strings.Contains(out, "Quiet windows") {
		t.Errorf("expected quiet-windows header, got:\n%s", out)
	}
	if !strings.Contains(out, "Busiest minute:") {
		t.Errorf("expected busiest-minute line, got:\n%s", out)
	}
}

func TestGapsEveryMinuteSaysNoGaps(t *testing.T) {
	out, _, err := runGaps(t, "* * * * *\n", "--tz", "UTC", "--quiet", "-")
	if err != nil {
		t.Fatalf("gaps: %v", err)
	}
	if !strings.Contains(out, "No quiet windows") {
		t.Errorf("expected no-quiet-windows message, got:\n%s", out)
	}
}

func TestGapsJSONShapeIsStable(t *testing.T) {
	out, _, err := runGaps(t, "0 12 * * *\n", "--tz", "UTC", "--days", "2", "--json", "-")
	if err != nil {
		t.Fatalf("gaps: %v", err)
	}
	var payload struct {
		Source string `json:"source"`
		From   string `json:"from"`
		To     string `json:"to"`
		Days   int    `json:"days"`
		Gaps   []struct {
			Start           string `json:"start"`
			End             string `json:"end"`
			DurationSeconds int64  `json:"duration_seconds"`
		} `json:"gaps"`
		Busiest struct {
			Time  string `json:"time"`
			Count int    `json:"count"`
		} `json:"busiest"`
		Skipped int `json:"skipped"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("json unmarshal: %v\nraw:\n%s", err, out)
	}
	if payload.Days != 2 {
		t.Errorf("days = %d, want 2", payload.Days)
	}
	if len(payload.Gaps) == 0 {
		t.Fatal("expected at least one gap in JSON")
	}
	if payload.Gaps[0].DurationSeconds <= 0 {
		t.Errorf("gap duration_seconds = %d, want > 0", payload.Gaps[0].DurationSeconds)
	}
	if payload.Busiest.Count != 1 {
		t.Errorf("busiest count = %d, want 1", payload.Busiest.Count)
	}
}

func TestGapsBadTimezoneErrors(t *testing.T) {
	_, _, err := runGaps(t, "0 12 * * *\n", "--tz", "Not/AZone", "--quiet", "-")
	if err == nil {
		t.Fatal("expected error for bad timezone")
	}
}

func TestGapsHumanizeSpan(t *testing.T) {
	if got := humanizeSpan((3*60 + 33) * 60 * 1_000_000_000); got != "3h33m" {
		t.Errorf("humanizeSpan 3h33m = %q", got)
	}
	if got := humanizeSpan(45 * 60 * 1_000_000_000); got != "45m" {
		t.Errorf("humanizeSpan 45m = %q", got)
	}
	if got := humanizeSpan(2 * 60 * 60 * 1_000_000_000); got != "2h" {
		t.Errorf("humanizeSpan 2h = %q", got)
	}
	if got := humanizeSpan(25 * 60 * 60 * 1_000_000_000); got != "1d1h" {
		t.Errorf("humanizeSpan 1d1h = %q", got)
	}
}
