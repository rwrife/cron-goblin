package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runStagger builds a fresh stagger command, optionally feeds stdin, and
// captures its streams. Mirrors runLint in lint_test.go.
func runStagger(t *testing.T, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := newStaggerCmd()
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

// writeTmp writes content to a temp crontab file and returns its path.
func writeTmp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "crontab.txt")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp crontab: %v", err)
	}
	return path
}

const herdCrontab = "# morning\n" +
	"0 9 * * * /backup\n" +
	"0 9 * * * /report\n" +
	"0 9 * * * /sync\n"

func TestStaggerHumanShowsSpread(t *testing.T) {
	path := writeTmp(t, herdCrontab)
	stdout, _, err := runStagger(t, "", "--quiet", "--max-spread", "30", path)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"Found 1 herd", "0 9 * * *", "anchor, unchanged", "→"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("human output missing %q, got:\n%s", want, stdout)
		}
	}
	// The proposed crontab is included and the second job moved off minute 0.
	if !strings.Contains(stdout, "Proposed crontab") {
		t.Errorf("missing proposed crontab block:\n%s", stdout)
	}
}

func TestStaggerDryRunDoesNotWriteFile(t *testing.T) {
	path := writeTmp(t, herdCrontab)
	if _, _, err := runStagger(t, "", "--quiet", path); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != herdCrontab {
		t.Errorf("dry run modified the file:\n%s", string(got))
	}
}

func TestStaggerJSONStable(t *testing.T) {
	path := writeTmp(t, herdCrontab)
	stdout, _, err := runStagger(t, "", "--quiet", "--json", "--max-spread", "30", path)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var report staggerReportJSON
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout)
	}
	if report.MaxSpread != 30 {
		t.Errorf("MaxSpread = %d, want 30", report.MaxSpread)
	}
	if len(report.Herds) != 1 || len(report.Herds[0].Moves) != 3 {
		t.Fatalf("unexpected herds shape: %+v", report.Herds)
	}
	if report.Moved != 2 {
		t.Errorf("Moved = %d, want 2", report.Moved)
	}
	if report.OK {
		t.Errorf("OK should be false when a herd exists")
	}
	if !strings.Contains(report.Rewritten, "9 * * * /report") {
		t.Errorf("rewritten crontab missing in JSON: %q", report.Rewritten)
	}
}

func TestStaggerNoHerdIsClean(t *testing.T) {
	clean := "0 9 * * * /a\n0 10 * * * /b\n"
	path := writeTmp(t, clean)
	stdout, _, err := runStagger(t, "", "--quiet", path)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(stdout, "No thundering herds") {
		t.Errorf("expected clean message, got:\n%s", stdout)
	}
}

func TestStaggerWriteRequiresConfirmation(t *testing.T) {
	path := writeTmp(t, herdCrontab)
	// Answer "n" → abort, file untouched.
	stdout, _, err := runStagger(t, "n\n", "--quiet", "--write", path)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(stdout, "Aborted") {
		t.Errorf("expected abort message, got:\n%s", stdout)
	}
	got, _ := os.ReadFile(path)
	if string(got) != herdCrontab {
		t.Errorf("file changed despite 'n':\n%s", string(got))
	}
}

func TestStaggerWriteEOFDefaultsToNo(t *testing.T) {
	path := writeTmp(t, herdCrontab)
	// No stdin (EOF immediately) must be treated as "no".
	if _, _, err := runStagger(t, "", "--quiet", "--write", path); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != herdCrontab {
		t.Errorf("EOF should not write the file:\n%s", string(got))
	}
}

func TestStaggerWriteYesApplies(t *testing.T) {
	path := writeTmp(t, herdCrontab)
	stdout, _, err := runStagger(t, "", "--quiet", "--write", "--yes", "--max-spread", "30", path)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(stdout, "Rewrote") {
		t.Errorf("expected rewrite confirmation, got:\n%s", stdout)
	}
	got, _ := os.ReadFile(path)
	out := string(got)
	if strings.Contains(out, "0 9 * * * /report") {
		t.Errorf("file was not staggered:\n%s", out)
	}
	// Comment preserved.
	if !strings.HasPrefix(out, "# morning\n") {
		t.Errorf("comment not preserved:\n%s", out)
	}
}

func TestStaggerWriteRefusesStdin(t *testing.T) {
	_, _, err := runStagger(t, "0 9 * * * /a\n0 9 * * * /b\n", "--quiet", "--write", "-")
	if err == nil {
		t.Fatal("expected error writing to stdin, got nil")
	}
}

func TestStaggerReadsStdin(t *testing.T) {
	stdout, _, err := runStagger(t, "0 9 * * * /a\n0 9 * * * /b\n", "--quiet", "--max-spread", "30")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(stdout, "Found 1 herd") {
		t.Errorf("stdin herd not detected:\n%s", stdout)
	}
}

func TestStaggerMissingFileErrors(t *testing.T) {
	_, _, err := runStagger(t, "", "--quiet", filepath.Join(t.TempDir(), "nope.txt"))
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestStaggerPersonaOnStderrByDefault(t *testing.T) {
	path := writeTmp(t, herdCrontab)
	_, stderr, err := runStagger(t, "", path) // no --quiet
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.TrimSpace(stderr) == "" {
		t.Error("expected goblin persona on stderr without --quiet")
	}
}

func TestStaggerNoHerdWriteSaysNothingToWrite(t *testing.T) {
	clean := "0 9 * * * /a\n0 10 * * * /b\n"
	path := writeTmp(t, clean)
	stdout, _, err := runStagger(t, "", "--quiet", "--write", "--yes", path)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(stdout, "already well spread") {
		t.Errorf("expected no-op write message, got:\n%s", stdout)
	}
}
