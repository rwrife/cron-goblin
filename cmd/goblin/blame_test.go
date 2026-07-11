package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runBlame builds a fresh blame command, optionally feeds stdin, and captures
// its streams. It mirrors runLint in lint_test.go.
func runBlame(t *testing.T, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := newBlameCmd()
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

const mixedCrontab = "# my crontab\n" +
	"SHELL=/bin/bash\n" +
	"\n" +
	"*/17 3 * * 1-5 /opt/report.sh\n" +
	"0 0 30 2 * /opt/never.sh\n" +
	"not a cron line\n" +
	"0 9 * * * /opt/daily.sh\n"

func TestBlameHumanMixed(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "crontab.txt")
	if err := os.WriteFile(tmp, []byte(mixedCrontab), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := runBlame(t, "", "--tz", "UTC", tmp)
	if err != nil {
		t.Fatalf("blame: %v", err)
	}
	if stderr == "" {
		t.Errorf("want grumpy stderr persona by default")
	}
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) != 7 {
		t.Fatalf("want 7 output lines, got %d:\n%s", len(lines), stdout)
	}
	// Comment/env/blank echoed verbatim.
	if lines[0] != "# my crontab" || lines[1] != "SHELL=/bin/bash" || lines[2] != "" {
		t.Errorf("non-schedule lines not preserved:\n%q\n%q\n%q", lines[0], lines[1], lines[2])
	}
	// Schedule line annotated with english + next.
	if !strings.Contains(lines[3], "# ") || !strings.Contains(lines[3], "next:") {
		t.Errorf("schedule line missing annotation: %q", lines[3])
	}
	if !strings.HasPrefix(lines[3], "*/17 3 * * 1-5 /opt/report.sh") {
		t.Errorf("schedule raw not preserved: %q", lines[3])
	}
	// Dead expression.
	if !strings.Contains(lines[4], "dead") || !strings.Contains(lines[4], "next: never") {
		t.Errorf("dead line not marked: %q", lines[4])
	}
	// Unparseable line passes through with a note.
	if !strings.HasPrefix(lines[5], "not a cron line") || !strings.Contains(lines[5], "#") {
		t.Errorf("unparseable line not noted: %q", lines[5])
	}
}

func TestBlameQuietSilencesStderr(t *testing.T) {
	_, stderr, err := runBlame(t, mixedCrontab, "--quiet", "-")
	if err != nil {
		t.Fatalf("blame: %v", err)
	}
	if stderr != "" {
		t.Errorf("--quiet should silence persona, got %q", stderr)
	}
}

func TestBlameJSON(t *testing.T) {
	stdout, _, err := runBlame(t, mixedCrontab, "--json", "--tz", "UTC", "-")
	if err != nil {
		t.Fatalf("blame: %v", err)
	}
	var rows []blameRowJSON
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("json: %v\n%s", err, stdout)
	}
	if len(rows) != 7 {
		t.Fatalf("want 7 rows, got %d", len(rows))
	}
	if rows[0].Line != 1 || rows[6].Line != 7 {
		t.Errorf("line numbers off: %d..%d", rows[0].Line, rows[6].Line)
	}
	// Schedule row populated.
	sched := rows[3]
	if sched.Schedule != "*/17 3 * * 1-5" || sched.English == "" || sched.Next == "" || sched.Dead {
		t.Errorf("schedule row wrong: %+v", sched)
	}
	// Dead row: dead=true, empty next.
	dead := rows[4]
	if !dead.Dead || dead.Next != "" {
		t.Errorf("dead row wrong: %+v", dead)
	}
	// Comment row: empty schedule/english.
	if rows[0].Schedule != "" || rows[0].English != "" {
		t.Errorf("comment row should be blank annotation: %+v", rows[0])
	}
}

func TestBlameBadTimezone(t *testing.T) {
	_, _, err := runBlame(t, "0 0 * * *\n", "--tz", "Nowhere/Fake", "-")
	if err == nil {
		t.Fatalf("want error for bad --tz")
	}
}
