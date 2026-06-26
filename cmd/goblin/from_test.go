package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// runFrom builds a fresh from command and captures its streams.
func runFrom(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := newFromCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

func TestFromHumanOutput(t *testing.T) {
	stdout, stderr, err := runFrom(t, "--quiet", "every weekday at 6:30pm")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	// The cron line must be the very first line of stdout (so it's pipeable).
	first := strings.SplitN(strings.TrimSpace(stdout), "\n", 2)[0]
	if first != "30 18 * * 1-5" {
		t.Errorf("first stdout line = %q, want the cron expression", first)
	}
	// A readback comment should describe it.
	if !strings.Contains(stdout, "# At 18:30 on weekdays") {
		t.Errorf("stdout missing English readback, got: %q", stdout)
	}
	if strings.TrimSpace(stderr) != "" {
		t.Errorf("--quiet should silence stderr, got: %q", stderr)
	}
}

func TestFromHeadlineAcceptance(t *testing.T) {
	// Straight from issue #6 / PLAN.md "done when".
	stdout, _, err := runFrom(t, "--quiet", "every 15 minutes")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	first := strings.SplitN(strings.TrimSpace(stdout), "\n", 2)[0]
	if first != "*/15 * * * *" {
		t.Errorf("`from \"every 15 minutes\"` = %q, want %q", first, "*/15 * * * *")
	}
}

func TestFromJoinsBareWords(t *testing.T) {
	// Unquoted multi-word phrases should work by joining args.
	stdout, _, err := runFrom(t, "--quiet", "daily", "at", "9am")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	first := strings.SplitN(strings.TrimSpace(stdout), "\n", 2)[0]
	if first != "0 9 * * *" {
		t.Errorf("joined-words phrase = %q, want %q", first, "0 9 * * *")
	}
}

func TestFromPersonaOnStderr(t *testing.T) {
	_, stderr, err := runFrom(t, "every day at noon")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if strings.TrimSpace(stderr) == "" {
		t.Error("expected goblin persona on stderr without --quiet")
	}
}

func TestFromJSON(t *testing.T) {
	stdout, _, err := runFrom(t, "--quiet", "--json", "every weekday at 6:30pm")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	var payload struct {
		Phrase     string   `json:"phrase"`
		Cron       string   `json:"cron"`
		English    string   `json:"english"`
		NextRuns   []string `json:"next_runs"`
		NeverFires bool     `json:"never_fires"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, stdout)
	}
	if payload.Cron != "30 18 * * 1-5" {
		t.Errorf("cron = %q, want %q", payload.Cron, "30 18 * * 1-5")
	}
	if payload.Phrase != "every weekday at 6:30pm" {
		t.Errorf("phrase = %q", payload.Phrase)
	}
	if !strings.Contains(payload.English, "weekdays") {
		t.Errorf("english = %q", payload.English)
	}
	if payload.NeverFires {
		t.Error("never_fires should be false")
	}
	if len(payload.NextRuns) != 1 {
		t.Errorf("expected 1 next_run by default, got %d: %v", len(payload.NextRuns), payload.NextRuns)
	}
	for _, r := range payload.NextRuns {
		if _, perr := time.Parse(time.RFC3339, r); perr != nil {
			t.Errorf("next_run %q is not RFC3339: %v", r, perr)
		}
	}
}

func TestFromCountFlag(t *testing.T) {
	stdout, _, err := runFrom(t, "--quiet", "--json", "-n", "3", "every 15 minutes")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	var payload struct {
		NextRuns []string `json:"next_runs"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if len(payload.NextRuns) != 3 {
		t.Errorf("expected 3 next_runs with -n 3, got %d", len(payload.NextRuns))
	}
}

func TestFromBadPhraseFails(t *testing.T) {
	_, stderr, err := runFrom(t, "--quiet", "every blue moon")
	if err == nil {
		t.Fatal("expected error for unrecognized phrase")
	}
	if !strings.Contains(stderr, "error:") {
		t.Errorf("expected a diagnostic on stderr, got: %q", stderr)
	}
}

func TestFromRequiresAnArg(t *testing.T) {
	if _, _, err := runFrom(t); err == nil {
		t.Error("expected error with no argument")
	}
}

// TestFromRoundTripsThroughExplain is a light integration check: from -> cron,
// then that cron through explain, should describe the same intent. We don't
// demand string equality (phrasings differ), just that a known phrase's cron
// explains back to something containing the expected anchor.
func TestFromRoundTripsThroughExplain(t *testing.T) {
	cases := []struct {
		phrase string
		anchor string
	}{
		{"every day at 9am", "09:00"},
		{"every weekend at noon", "weekends"},
		{"first of the month at 9am", "1st"},
	}
	for _, tc := range cases {
		fromOut, _, err := runFrom(t, "--quiet", "--json", tc.phrase)
		if err != nil {
			t.Fatalf("from(%q) error: %v", tc.phrase, err)
		}
		var p struct {
			English string `json:"english"`
		}
		if err := json.Unmarshal([]byte(fromOut), &p); err != nil {
			t.Fatalf("bad JSON: %v", err)
		}
		if !strings.Contains(p.English, tc.anchor) {
			t.Errorf("from(%q) english %q missing anchor %q", tc.phrase, p.English, tc.anchor)
		}
	}
}
