package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// runNext builds a fresh next command and captures its streams.
func runNext(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := newNextCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

func TestNextHumanOutputListsRuns(t *testing.T) {
	stdout, _, err := runNext(t, "--quiet", "--tz", "UTC", "-n", "3", "*/15 * * * *")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !strings.Contains(stdout, "*/15 * * * *") {
		t.Errorf("stdout missing echoed expression, got: %q", stdout)
	}
	// Three indented RFC3339 lines expected.
	lines := 0
	for _, ln := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(ln, "  ") && strings.Contains(ln, "T") {
			if _, perr := time.Parse(time.RFC3339, strings.TrimSpace(ln)); perr == nil {
				lines++
			}
		}
	}
	if lines != 3 {
		t.Errorf("expected 3 fire-time lines, got %d in:\n%s", lines, stdout)
	}
}

func TestNextJSON(t *testing.T) {
	stdout, _, err := runNext(t, "--quiet", "--json", "--tz", "UTC", "-n", "4", "*/15 * * * *")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	var payload struct {
		Expression string   `json:"expression"`
		English    string   `json:"english"`
		Timezone   string   `json:"timezone"`
		Count      int      `json:"count"`
		NextRuns   []string `json:"next_runs"`
		NeverFires bool     `json:"never_fires"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, stdout)
	}
	if payload.Timezone != "UTC" {
		t.Errorf("timezone = %q, want UTC", payload.Timezone)
	}
	if payload.Count != 4 || len(payload.NextRuns) != 4 {
		t.Errorf("count/next_runs mismatch: count=%d runs=%d", payload.Count, len(payload.NextRuns))
	}
	if payload.NeverFires {
		t.Error("never_fires should be false for a live schedule")
	}
	for _, r := range payload.NextRuns {
		ts, perr := time.Parse(time.RFC3339, r)
		if perr != nil {
			t.Errorf("next_run %q not RFC3339: %v", r, perr)
			continue
		}
		if ts.Minute()%15 != 0 {
			t.Errorf("next_run %q minute not a multiple of 15", r)
		}
	}
}

func TestNextDeadExpressionReported(t *testing.T) {
	// Human form.
	stdout, _, err := runNext(t, "--quiet", "--tz", "UTC", "0 0 30 2 *")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !strings.Contains(strings.ToLower(stdout), "never fires") {
		t.Errorf("expected a never-fires notice, got: %q", stdout)
	}

	// JSON form: never_fires true and empty next_runs.
	jsonOut, _, err := runNext(t, "--quiet", "--json", "0 0 30 2 *")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	var payload struct {
		NextRuns   []string `json:"next_runs"`
		NeverFires bool     `json:"never_fires"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &payload); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, jsonOut)
	}
	if !payload.NeverFires || len(payload.NextRuns) != 0 {
		t.Errorf("dead expr JSON wrong: never_fires=%v runs=%v", payload.NeverFires, payload.NextRuns)
	}
}

func TestNextRejectsBadTimezone(t *testing.T) {
	_, stderr, err := runNext(t, "--quiet", "--tz", "Mars/Olympus_Mons", "* * * * *")
	if err == nil {
		t.Fatal("expected error for unknown timezone")
	}
	if !strings.Contains(stderr, "timezone") {
		t.Errorf("expected timezone diagnostic, got: %q", stderr)
	}
}

func TestNextRejectsNonPositiveCount(t *testing.T) {
	if _, _, err := runNext(t, "--quiet", "-n", "0", "* * * * *"); err == nil {
		t.Error("expected error for -n 0")
	}
}

func TestNextBadExpressionFails(t *testing.T) {
	_, stderr, err := runNext(t, "--quiet", "not a cron")
	if err == nil {
		t.Fatal("expected error for malformed expression")
	}
	if !strings.Contains(stderr, "error:") {
		t.Errorf("expected a diagnostic on stderr, got: %q", stderr)
	}
}

func TestNextPersonaOnStderr(t *testing.T) {
	_, stderr, err := runNext(t, "--tz", "UTC", "0 0 * * *")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if strings.TrimSpace(stderr) == "" {
		t.Error("expected goblin persona on stderr without --quiet")
	}
}
