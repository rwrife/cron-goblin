package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rwrife/cron-goblin/internal/parse"
)

// runWatch builds a fresh watch command and captures its streams. stdin can be
// supplied for the pipe/file paths; pass "" for none.
func runWatch(t *testing.T, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := newWatchCmd()
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

// mustParse is a tiny helper for building watchJobs in the pure-function tests.
func mustParse(t *testing.T, expr string) parse.Schedule {
	t.Helper()
	s, err := parse.Parse(expr)
	if err != nil {
		t.Fatalf("parse %q: %v", expr, err)
	}
	return s
}

// --- Acceptance: `watch --once --expr` prints one frame + positive countdown ---

func TestWatchOnceSingleExprOneFrame(t *testing.T) {
	stdout, _, err := runWatch(t, "", "--once", "--quiet", "--tz", "UTC", "--expr", "*/5 * * * *")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	// Exactly one header line => one frame.
	if n := strings.Count(stdout, "goblin watch —"); n != 1 {
		t.Errorf("expected exactly one frame header, got %d\n%s", n, stdout)
	}
	// The one row carries the expression and a positive "in ...s" countdown.
	if !strings.Contains(stdout, "*/5 * * * *") {
		t.Errorf("frame missing the expression row:\n%s", stdout)
	}
	if !strings.Contains(stdout, " in ") {
		t.Errorf("frame missing a positive countdown (expected an 'in ...' cell):\n%s", stdout)
	}
	// No ANSI clear-screen in the --once (script) path.
	if strings.Contains(stdout, "\x1b[2J") {
		t.Errorf("--once output must not contain ANSI clear-screen sequences:\n%q", stdout)
	}
}

// --- Acceptance: file/stdin with multiple jobs, sorted by soonest next-fire ---

func TestWatchMultiJobSortedSoonestFirst(t *testing.T) {
	// A crontab with (deliberately) out-of-order jobs plus comments/env lines to
	// prove the lint-style parser skips them.
	crontab := "# my crontab\n" +
		"SHELL=/bin/bash\n" +
		"0 9 * * 1-5 /bin/weekday\n" +
		"*/2 * * * * /bin/soon\n" +
		"30 3 * * * /bin/nightly\n"

	stdout, _, err := runWatch(t, crontab, "--once", "--quiet", "--tz", "UTC", "-")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	if !strings.Contains(stdout, "3 job(s)") {
		t.Errorf("expected 3 jobs (comment/env lines skipped), got:\n%s", stdout)
	}

	// The soonest cadence (*/2) must appear before the two daily jobs.
	iSoon := strings.Index(stdout, "*/2 * * * *")
	iNightly := strings.Index(stdout, "30 3 * * *")
	iWeekday := strings.Index(stdout, "0 9 * * 1-5")
	if iSoon < 0 || iNightly < 0 || iWeekday < 0 {
		t.Fatalf("missing an expected row:\n%s", stdout)
	}
	if !(iSoon < iNightly && iSoon < iWeekday) {
		t.Errorf("rows not sorted soonest-first (soon=%d nightly=%d weekday=%d)\n%s",
			iSoon, iNightly, iWeekday, stdout)
	}
}

// --- Acceptance: never-fires jobs render as `never` and sink to the bottom ---

func TestWatchNeverFiresSinksToBottom(t *testing.T) {
	crontab := "0 0 30 2 * /bin/never\n" + // Feb 30 — never fires
		"*/5 * * * * /bin/soon\n"

	stdout, _, err := runWatch(t, crontab, "--once", "--quiet", "--tz", "UTC", "-")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !strings.Contains(stdout, "never") {
		t.Errorf("expected a 'never' cell for the dead expression:\n%s", stdout)
	}
	// The live (soon) job must sort above the never-fires one.
	iSoon := strings.Index(stdout, "*/5 * * * *")
	iNever := strings.Index(stdout, "0 0 30 2 *")
	if iSoon < 0 || iNever < 0 {
		t.Fatalf("missing an expected row:\n%s", stdout)
	}
	if iSoon > iNever {
		t.Errorf("never-fires job should sink below firing jobs (soon=%d never=%d)\n%s",
			iSoon, iNever, stdout)
	}
}

// --- Acceptance: --tz honored (display zone changes the printed timestamps) ---

func TestWatchTZHonored(t *testing.T) {
	utc, _, err := runWatch(t, "", "--once", "--quiet", "--tz", "UTC", "--expr", "0 12 * * *")
	if err != nil {
		t.Fatalf("UTC Execute() error: %v", err)
	}
	ny, _, err := runWatch(t, "", "--once", "--quiet", "--tz", "America/New_York", "--expr", "0 12 * * *")
	if err != nil {
		t.Fatalf("NY Execute() error: %v", err)
	}
	if !strings.Contains(utc, "UTC") {
		t.Errorf("UTC frame should label the zone as UTC:\n%s", utc)
	}
	// New York is EST/EDT, never "UTC"; the abbreviation must differ.
	if strings.Contains(ny, " UTC ") || strings.Contains(ny, " UTC\n") {
		t.Errorf("America/New_York frame should not be labeled UTC:\n%s", ny)
	}
}

func TestWatchRejectsBadTimezone(t *testing.T) {
	_, stderr, err := runWatch(t, "", "--once", "--quiet", "--tz", "Mars/Olympus_Mons", "--expr", "* * * * *")
	if err == nil {
		t.Fatal("expected error for unknown timezone")
	}
	if !strings.Contains(stderr, "timezone") {
		t.Errorf("expected a timezone diagnostic, got: %q", stderr)
	}
}

func TestWatchRejectsBadExpression(t *testing.T) {
	_, stderr, err := runWatch(t, "", "--once", "--quiet", "--expr", "not a cron")
	if err == nil {
		t.Fatal("expected error for malformed --expr")
	}
	if !strings.Contains(stderr, "error:") {
		t.Errorf("expected a diagnostic on stderr, got: %q", stderr)
	}
}

func TestWatchRejectsBadCrontabLine(t *testing.T) {
	// A malformed schedule line should surface, not be silently dropped.
	_, stderr, err := runWatch(t, "this is not valid\n", "--once", "--quiet", "-")
	if err == nil {
		t.Fatal("expected error for a malformed crontab line")
	}
	if !strings.Contains(stderr, "error:") {
		t.Errorf("expected a diagnostic on stderr, got: %q", stderr)
	}
}

func TestWatchRejectsExprAndFileTogether(t *testing.T) {
	_, stderr, err := runWatch(t, "", "--once", "--quiet", "--expr", "* * * * *", "some-file.txt")
	if err == nil {
		t.Fatal("expected error when both --expr and a file are given")
	}
	if !strings.Contains(stderr, "either") {
		t.Errorf("expected an either/or diagnostic, got: %q", stderr)
	}
}

func TestWatchRejectsEmptyInput(t *testing.T) {
	// Only comments/blank lines => nothing to watch.
	_, stderr, err := runWatch(t, "# nothing here\n\n", "--once", "--quiet", "-")
	if err == nil {
		t.Fatal("expected error when there are no schedules to watch")
	}
	if !strings.Contains(stderr, "nothing to watch") {
		t.Errorf("expected a 'nothing to watch' diagnostic, got: %q", stderr)
	}
}

func TestWatchRejectsNonPositiveInterval(t *testing.T) {
	if _, _, err := runWatch(t, "", "--quiet", "--interval", "0", "--expr", "* * * * *"); err == nil {
		t.Error("expected error for --interval 0")
	}
}

func TestWatchReadsFileArgument(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "crontab.txt")
	if err := os.WriteFile(path, []byte("*/5 * * * * /bin/thing\n"), 0o644); err != nil {
		t.Fatalf("writing temp crontab: %v", err)
	}
	stdout, _, err := runWatch(t, "", "--once", "--quiet", "--tz", "UTC", path)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !strings.Contains(stdout, "1 job(s)") || !strings.Contains(stdout, "*/5 * * * *") {
		t.Errorf("file argument not read correctly:\n%s", stdout)
	}
}

func TestWatchPersonaOnStderr(t *testing.T) {
	_, stderr, err := runWatch(t, "", "--once", "--tz", "UTC", "--expr", "0 0 * * *")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if strings.TrimSpace(stderr) == "" {
		t.Error("expected goblin persona on stderr without --quiet")
	}
}

func TestWatchQuietSuppressesPersona(t *testing.T) {
	_, stderr, err := runWatch(t, "", "--once", "--quiet", "--tz", "UTC", "--expr", "0 0 * * *")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if strings.TrimSpace(stderr) != "" {
		t.Errorf("--quiet should silence stderr persona, got: %q", stderr)
	}
}

// --- Pure-function coverage: humanizeCountdown ---

func TestHumanizeCountdown(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "now"},
		{-5 * time.Second, "now"},
		{45 * time.Second, "in 45s"},
		{90 * time.Second, "in 1m 30s"},
		{time.Hour + 2*time.Minute + 3*time.Second, "in 1h 2m 3s"},
		{25 * time.Hour, "in 1d 1h 0m 0s"},
		{500 * time.Millisecond, "now"}, // sub-second rounds down to 0
	}
	for _, c := range cases {
		if got := humanizeCountdown(c.d); got != c.want {
			t.Errorf("humanizeCountdown(%s) = %q, want %q", c.d, got, c.want)
		}
	}
}

// --- Pure-function coverage: computeRows sorting/never-fires semantics ---

func TestComputeRowsSortsAndFlagsNeverFires(t *testing.T) {
	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	jobs := []watchJob{
		{Raw: "0 9 * * 1-5", Schedule: mustParse(t, "0 9 * * 1-5")},
		{Raw: "0 0 30 2 *", Schedule: mustParse(t, "0 0 30 2 *")}, // never
		{Raw: "*/2 * * * *", Schedule: mustParse(t, "*/2 * * * *")},
	}
	rows := computeRows(jobs, now, time.UTC)
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
	// Soonest first.
	if rows[0].Raw != "*/2 * * * *" {
		t.Errorf("expected the every-2-min job first, got %q", rows[0].Raw)
	}
	// Never-fires last and flagged.
	last := rows[len(rows)-1]
	if last.Raw != "0 0 30 2 *" || !last.NeverFires {
		t.Errorf("expected the dead expression last and flagged NeverFires, got %+v", last)
	}
	// Firing rows are in ascending next-fire order.
	for i := 1; i < len(rows); i++ {
		if rows[i-1].NeverFires || rows[i].NeverFires {
			continue
		}
		if rows[i].Next.Before(rows[i-1].Next) {
			t.Errorf("rows not ascending by next-fire at %d: %s before %s",
				i, rows[i].Next, rows[i-1].Next)
		}
	}
}

// --- Pure-function coverage: renderFrame clear-screen prefix toggling ---

func TestRenderFrameClearScreenToggle(t *testing.T) {
	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	jobs := []watchJob{{Raw: "*/5 * * * *", Schedule: mustParse(t, "*/5 * * * *")}}

	plain := renderFrame(jobs, now, time.UTC, false)
	if strings.Contains(plain, "\x1b[2J") {
		t.Errorf("clear=false frame must not contain ANSI clear-screen:\n%q", plain)
	}
	if !strings.HasPrefix(plain, "goblin watch —") {
		t.Errorf("clear=false frame should start with the header, got: %.40q", plain)
	}

	cleared := renderFrame(jobs, now, time.UTC, true)
	if !strings.HasPrefix(cleared, "\x1b[H\x1b[2J") {
		t.Errorf("clear=true frame must start with cursor-home + clear-screen, got: %.20q", cleared)
	}
}
