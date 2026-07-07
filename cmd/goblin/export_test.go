package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runExport builds a fresh export command and captures its streams.
func runExport(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := newExportCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

// countLines counts occurrences of an exact CRLF-terminated content line.
func countLines(s, prefix string) int {
	n := 0
	for _, ln := range strings.Split(s, "\r\n") {
		if strings.HasPrefix(ln, prefix) {
			n++
		}
	}
	return n
}

func TestExportEmitsValidCalendar(t *testing.T) {
	stdout, _, err := runExport(t, "--quiet", "--tz", "UTC", "-n", "5", "0 9 * * 1-5")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	// Structural envelope.
	if !strings.HasPrefix(stdout, "BEGIN:VCALENDAR\r\n") {
		t.Errorf("calendar must start with BEGIN:VCALENDAR + CRLF, got: %.40q", stdout)
	}
	if !strings.HasSuffix(stdout, "END:VCALENDAR\r\n") {
		t.Errorf("calendar must end with END:VCALENDAR + CRLF, got tail: %.40q",
			stdout[max(0, len(stdout)-40):])
	}
	for _, must := range []string{"VERSION:2.0", "PRODID:", "CALSCALE:GREGORIAN"} {
		if !strings.Contains(stdout, must) {
			t.Errorf("calendar missing required header %q", must)
		}
	}

	// Exactly five events, each with the mandatory properties.
	if got := countLines(stdout, "BEGIN:VEVENT"); got != 5 {
		t.Errorf("expected 5 VEVENTs, got %d", got)
	}
	if got := countLines(stdout, "END:VEVENT"); got != 5 {
		t.Errorf("expected 5 END:VEVENTs, got %d", got)
	}
	if got := countLines(stdout, "UID:"); got != 5 {
		t.Errorf("expected 5 UIDs, got %d", got)
	}
	if got := countLines(stdout, "DTSTART:"); got != 5 {
		t.Errorf("expected 5 DTSTARTs, got %d", got)
	}
}

func TestExportDTStartIsUTCAndParses(t *testing.T) {
	stdout, _, err := runExport(t, "--quiet", "--tz", "America/New_York", "-n", "3", "0 9 * * 1-5")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	found := 0
	for _, ln := range strings.Split(stdout, "\r\n") {
		v, ok := strings.CutPrefix(ln, "DTSTART:")
		if !ok {
			continue
		}
		found++
		if !strings.HasSuffix(v, "Z") {
			t.Errorf("DTSTART %q should be UTC (end in Z)", v)
		}
		if _, perr := time.Parse("20060102T150405Z", v); perr != nil {
			t.Errorf("DTSTART %q not a valid iCal UTC datetime: %v", v, perr)
		}
	}
	if found != 3 {
		t.Errorf("expected 3 DTSTART lines, got %d", found)
	}
}

func TestExportDefaultSummaryIsEnglish(t *testing.T) {
	// Without --summary the event title should be the plain-English explanation.
	stdout, _, err := runExport(t, "--quiet", "--tz", "UTC", "-n", "1", "0 9 * * 1-5")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !strings.Contains(stdout, "SUMMARY:") {
		t.Fatalf("no SUMMARY line in output:\n%s", stdout)
	}
	// A custom summary overrides it.
	custom, _, err := runExport(t, "--quiet", "--tz", "UTC", "-n", "1", "--summary", "nightly backup", "0 9 * * 1-5")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !strings.Contains(custom, "SUMMARY:nightly backup") {
		t.Errorf("custom --summary not honored, got:\n%s", custom)
	}
}

func TestExportDurationSetsDTEnd(t *testing.T) {
	// With --duration the DTEND should be duration after DTSTART.
	stdout, _, err := runExport(t, "--quiet", "--tz", "UTC", "-n", "1", "--duration", "30m", "0 9 * * 1-5")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	var start, end time.Time
	for _, ln := range strings.Split(stdout, "\r\n") {
		if v, ok := strings.CutPrefix(ln, "DTSTART:"); ok {
			start, _ = time.Parse("20060102T150405Z", v)
		}
		if v, ok := strings.CutPrefix(ln, "DTEND:"); ok {
			end, _ = time.Parse("20060102T150405Z", v)
		}
	}
	if start.IsZero() || end.IsZero() {
		t.Fatalf("missing DTSTART/DTEND in:\n%s", stdout)
	}
	if got := end.Sub(start); got != 30*time.Minute {
		t.Errorf("DTEND-DTSTART = %s, want 30m", got)
	}
}

func TestExportEscapesTextValues(t *testing.T) {
	// Commas/semicolons in the summary must be backslash-escaped per RFC 5545.
	stdout, _, err := runExport(t, "--quiet", "--tz", "UTC", "-n", "1",
		"--summary", "warm; then, sweep", "*/15 * * * *")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !strings.Contains(stdout, `SUMMARY:warm\; then\, sweep`) {
		t.Errorf("summary special chars not escaped, got:\n%s", stdout)
	}
}

func TestExportFoldsLongLines(t *testing.T) {
	// A very long summary forces line folding; no content line may exceed 75
	// octets, and continuation lines start with a single space.
	longSummary := strings.Repeat("goblin ", 40) // ~280 chars
	stdout, _, err := runExport(t, "--quiet", "--tz", "UTC", "-n", "1",
		"--summary", longSummary, "0 9 * * *")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	lines := strings.Split(stdout, "\r\n")
	sawFold := false
	for _, ln := range lines {
		if len(ln) > 75 {
			t.Errorf("content line exceeds 75 octets (%d): %.80q", len(ln), ln)
		}
		if strings.HasPrefix(ln, " ") {
			sawFold = true
		}
	}
	if !sawFold {
		t.Error("expected at least one folded continuation line for a long summary")
	}
}

func TestExportWritesFileWithDashO(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sched.ics")
	stdout, _, err := runExport(t, "--quiet", "--tz", "UTC", "-n", "2", "-o", path, "0 9 * * 1-5")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if stdout != "" {
		t.Errorf("-o should print nothing to stdout, got: %q", stdout)
	}
	data, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatalf("reading written file: %v", rerr)
	}
	if !bytes.HasPrefix(data, []byte("BEGIN:VCALENDAR")) {
		t.Errorf("written file is not an iCalendar, starts: %.30q", data)
	}
	if n := bytes.Count(data, []byte("BEGIN:VEVENT")); n != 2 {
		t.Errorf("written file should have 2 events, got %d", n)
	}
}

func TestExportNeverFiresEmptyCalendar(t *testing.T) {
	// A dead expression yields a valid but event-free calendar and a warning.
	stdout, stderr, err := runExport(t, "--tz", "UTC", "0 0 30 2 *")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if strings.Contains(stdout, "BEGIN:VEVENT") {
		t.Errorf("never-fires expression should have no events, got:\n%s", stdout)
	}
	if !strings.HasPrefix(stdout, "BEGIN:VCALENDAR") || !strings.HasSuffix(stdout, "END:VCALENDAR\r\n") {
		t.Errorf("empty calendar still must be well-formed, got:\n%s", stdout)
	}
	if !strings.Contains(strings.ToLower(stderr), "never fires") {
		t.Errorf("expected a never-fires warning on stderr, got: %q", stderr)
	}
}

func TestExportRejectsBadTimezone(t *testing.T) {
	_, stderr, err := runExport(t, "--quiet", "--tz", "Mars/Olympus_Mons", "* * * * *")
	if err == nil {
		t.Fatal("expected error for unknown timezone")
	}
	if !strings.Contains(stderr, "timezone") {
		t.Errorf("expected timezone diagnostic, got: %q", stderr)
	}
}

func TestExportRejectsBadExpression(t *testing.T) {
	_, stderr, err := runExport(t, "--quiet", "not a cron")
	if err == nil {
		t.Fatal("expected error for malformed expression")
	}
	if !strings.Contains(stderr, "error:") {
		t.Errorf("expected a diagnostic on stderr, got: %q", stderr)
	}
}

func TestExportRejectsNonPositiveCount(t *testing.T) {
	if _, _, err := runExport(t, "--quiet", "-n", "0", "* * * * *"); err == nil {
		t.Error("expected error for -n 0")
	}
}

func TestExportPersonaOnStderr(t *testing.T) {
	_, stderr, err := runExport(t, "--tz", "UTC", "-n", "1", "0 0 * * *")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if strings.TrimSpace(stderr) == "" {
		t.Error("expected goblin persona on stderr without --quiet")
	}
}
