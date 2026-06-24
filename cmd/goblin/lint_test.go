package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runLint builds a fresh lint command, optionally feeds stdin, and captures
// its streams. It mirrors runNext in next_test.go.
func runLint(t *testing.T, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := newLintCmd()
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

// writeTempCrontab writes content to a temp file and returns its path.
func writeTempCrontab(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "crontab.txt")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp crontab: %v", err)
	}
	return path
}

const sampleCrontab = `# example
SHELL=/bin/bash
0 3 * * * /bin/backup
0 3 * * * /bin/logrotate
* * * * * /bin/spin
0 0 30 2 * /bin/never
30 6 * * 1-5 /bin/report
`

func TestLintHumanReportsAllRules(t *testing.T) {
	path := writeTempCrontab(t, sampleCrontab)
	stdout, _, err := runLint(t, "", "--quiet", path)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	for _, want := range []string{"thundering herd", "fires every minute", "never fires", "5 schedule(s)"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("human output missing %q, got:\n%s", want, stdout)
		}
	}
	if !strings.Contains(stdout, "1 error(s), 2 warning(s)") {
		t.Errorf("summary line wrong, got:\n%s", stdout)
	}
}

func TestLintJSONStable(t *testing.T) {
	path := writeTempCrontab(t, sampleCrontab)
	stdout, _, err := runLint(t, "", "--quiet", "--json", path)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	var payload struct {
		Source   string `json:"source"`
		Entries  int    `json:"entries"`
		Findings []struct {
			Rule     string `json:"rule"`
			Severity string `json:"severity"`
			Message  string `json:"message"`
			Lines    []int  `json:"lines"`
		} `json:"findings"`
		Counts struct {
			Info    int `json:"info"`
			Warning int `json:"warning"`
			Error   int `json:"error"`
		} `json:"counts"`
		Worst string `json:"worst"`
		OK    bool   `json:"ok"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, stdout)
	}
	if payload.Entries != 5 {
		t.Errorf("entries = %d, want 5", payload.Entries)
	}
	if payload.Counts.Error != 1 || payload.Counts.Warning != 2 {
		t.Errorf("counts wrong: %+v", payload.Counts)
	}
	if payload.Worst != "error" {
		t.Errorf("worst = %q, want error", payload.Worst)
	}
	if payload.OK {
		t.Error("ok should be false when errors/warnings present")
	}
	// Verify the rules we expect are all present.
	rules := map[string]bool{}
	for _, f := range payload.Findings {
		rules[f.Rule] = true
		if f.Lines == nil {
			t.Errorf("finding %q has null lines (should be [] at least)", f.Rule)
		}
	}
	for _, want := range []string{"collision", "too-frequent", "dead-expression"} {
		if !rules[want] {
			t.Errorf("JSON missing expected rule %q; got rules %v", want, rules)
		}
	}
}

func TestLintCIExitsNonZero(t *testing.T) {
	path := writeTempCrontab(t, sampleCrontab)
	_, _, err := runLint(t, "", "--quiet", "--ci", path)
	if err == nil {
		t.Fatal("--ci with warnings/errors should return an error (non-zero exit)")
	}
}

func TestLintCICleanExitsZero(t *testing.T) {
	clean := "0 3 * * * /bin/backup\n30 6 * * 1-5 /bin/report\n"
	path := writeTempCrontab(t, clean)
	stdout, _, err := runLint(t, "", "--quiet", "--ci", path)
	if err != nil {
		t.Fatalf("--ci on clean crontab should succeed, got: %v", err)
	}
	if !strings.Contains(stdout, "No problems found") {
		t.Errorf("expected clean message, got:\n%s", stdout)
	}
}

func TestLintReadsStdin(t *testing.T) {
	stdout, _, err := runLint(t, sampleCrontab, "--quiet", "-")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !strings.Contains(stdout, "<stdin>") {
		t.Errorf("expected <stdin> source label, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "never fires") {
		t.Errorf("stdin lint missing dead-expr finding, got:\n%s", stdout)
	}
}

func TestLintMissingFileErrors(t *testing.T) {
	_, stderr, err := runLint(t, "", "--quiet", "/no/such/crontab/file.txt")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(stderr, "error:") {
		t.Errorf("expected error on stderr, got: %q", stderr)
	}
}

func TestLintPersonaOnStderrByDefault(t *testing.T) {
	path := writeTempCrontab(t, sampleCrontab)
	stdout, stderr, err := runLint(t, "", path) // no --quiet
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if strings.TrimSpace(stderr) == "" {
		t.Error("expected goblin persona on stderr without --quiet")
	}
	// Persona must not pollute stdout (scripts read stdout).
	if strings.Contains(stdout, "crontab") && strings.Contains(stdout, "opinions") {
		t.Error("persona leaked into stdout")
	}
}
