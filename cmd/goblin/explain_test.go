package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// runExplain builds a fresh explain command and captures its streams.
func runExplain(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := newExplainCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

func TestExplainHumanOutput(t *testing.T) {
	stdout, stderr, err := runExplain(t, "--quiet", "30 6 * * 1-5")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !strings.Contains(stdout, "At 06:30 on weekdays") {
		t.Errorf("stdout missing English description, got: %q", stdout)
	}
	if !strings.Contains(stdout, "30 6 * * 1-5") {
		t.Errorf("stdout missing echoed expression, got: %q", stdout)
	}
	if strings.TrimSpace(stderr) != "" {
		t.Errorf("--quiet should silence stderr, got: %q", stderr)
	}
}

func TestExplainPersonaOnStderr(t *testing.T) {
	_, stderr, err := runExplain(t, "0 0 * * *")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if strings.TrimSpace(stderr) == "" {
		t.Error("expected goblin persona on stderr without --quiet")
	}
}

func TestExplainJSON(t *testing.T) {
	stdout, _, err := runExplain(t, "--quiet", "--json", "*/15 9-17 * * 1-5")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	var payload struct {
		Expression string `json:"expression"`
		English    string `json:"english"`
		Fields     struct {
			Minute    []int `json:"minute"`
			Hour      []int `json:"hour"`
			DayOfWeek []int `json:"day_of_week"`
		} `json:"fields"`
		NextRuns    []string `json:"next_runs"`
		NextRunNote string   `json:"next_runs_note"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, stdout)
	}
	if payload.Expression != "*/15 9-17 * * 1-5" {
		t.Errorf("expression = %q", payload.Expression)
	}
	if !strings.Contains(payload.English, "Every 15 minutes") {
		t.Errorf("english = %q", payload.English)
	}
	if len(payload.Fields.Minute) != 4 || len(payload.Fields.Hour) != 9 {
		t.Errorf("field value sets unexpected: %+v", payload.Fields)
	}
	if len(payload.Fields.DayOfWeek) != 5 {
		t.Errorf("day_of_week = %v, want 5 entries", payload.Fields.DayOfWeek)
	}
	if len(payload.NextRuns) != 0 || payload.NextRunNote == "" {
		t.Errorf("expected empty next_runs with a note, got runs=%v note=%q",
			payload.NextRuns, payload.NextRunNote)
	}
}

func TestExplainBadExpressionFails(t *testing.T) {
	_, stderr, err := runExplain(t, "--quiet", "* * * *")
	if err == nil {
		t.Fatal("expected error for malformed expression")
	}
	if !strings.Contains(stderr, "error:") || !strings.Contains(stderr, "5 fields") {
		t.Errorf("expected a diagnostic on stderr, got: %q", stderr)
	}
}

func TestExplainRequiresExactlyOneArg(t *testing.T) {
	if _, _, err := runExplain(t); err == nil {
		t.Error("expected error with no argument")
	}
	if _, _, err := runExplain(t, "a", "b"); err == nil {
		t.Error("expected error with two arguments")
	}
}
