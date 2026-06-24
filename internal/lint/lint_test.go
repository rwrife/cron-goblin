package lint

import (
	"strings"
	"testing"
)

// findingsByRule indexes a report's findings by rule code for easy assertions.
func findingsByRule(r Report) map[string][]Finding {
	m := map[string][]Finding{}
	for _, f := range r.Findings {
		m[f.Rule] = append(m[f.Rule], f)
	}
	return m
}

func TestParseCrontabSkipsNonSchedules(t *testing.T) {
	in := `# a comment
SHELL=/bin/bash
PATH=/usr/bin:/bin
MAILTO=""

0 3 * * * /usr/local/bin/backup.sh
`
	entries, err := ParseCrontab(strings.NewReader(in))
	if err != nil {
		t.Fatalf("ParseCrontab error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 schedule entry, got %d: %+v", len(entries), entries)
	}
	e := entries[0]
	if e.Command != "/usr/local/bin/backup.sh" {
		t.Errorf("command = %q, want backup.sh", e.Command)
	}
	if e.Line != 6 {
		t.Errorf("line = %d, want 6", e.Line)
	}
	if e.ParseErr != nil {
		t.Errorf("unexpected ParseErr: %v", e.ParseErr)
	}
}

func TestEnvAssignmentDetection(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{"SHELL=/bin/bash", true},
		{"PATH=/usr/bin:/bin", true},
		{`MAILTO=""`, true},
		{"FOO_BAR=baz", true},
		{"0 3 * * * cmd VAR=1", false}, // '=' is in the command, not a name
		{"* * * * * echo hi", false},
		{"=oops", false},
	}
	for _, c := range cases {
		if got := isEnvAssignment(c.line); got != c.want {
			t.Errorf("isEnvAssignment(%q) = %v, want %v", c.line, got, c.want)
		}
	}
}

func TestDeadExpressionRule(t *testing.T) {
	in := "0 0 30 2 * /bin/never\n0 3 * * * /bin/ok\n"
	report, err := Lint(strings.NewReader(in), nil)
	if err != nil {
		t.Fatalf("Lint error: %v", err)
	}
	dead := findingsByRule(report)["dead-expression"]
	if len(dead) != 1 {
		t.Fatalf("expected 1 dead-expression finding, got %d", len(dead))
	}
	if dead[0].Severity != SeverityError {
		t.Errorf("dead severity = %v, want error", dead[0].Severity)
	}
	if len(dead[0].Lines) != 1 || dead[0].Lines[0] != 1 {
		t.Errorf("dead lines = %v, want [1]", dead[0].Lines)
	}
}

func TestTooFrequentRule(t *testing.T) {
	in := "* * * * * /bin/spin\n0 0 30 2 * /bin/never\n*/5 * * * * /bin/fine\n"
	report, err := Lint(strings.NewReader(in), nil)
	if err != nil {
		t.Fatalf("Lint error: %v", err)
	}
	freq := findingsByRule(report)["too-frequent"]
	if len(freq) != 1 {
		t.Fatalf("expected exactly 1 too-frequent finding (every-minute only), got %d: %+v", len(freq), freq)
	}
	if freq[0].Lines[0] != 1 {
		t.Errorf("too-frequent line = %v, want [1]", freq[0].Lines)
	}
	if freq[0].Severity != SeverityWarning {
		t.Errorf("too-frequent severity = %v, want warning", freq[0].Severity)
	}
}

func TestCollisionRuleSameMinute(t *testing.T) {
	// The issue's canonical case: two jobs at 0 3 * * * must collide.
	in := "0 3 * * * /bin/backup\n0 3 * * * /bin/logrotate\n30 6 * * * /bin/solo\n"
	report, err := Lint(strings.NewReader(in), nil)
	if err != nil {
		t.Fatalf("Lint error: %v", err)
	}
	coll := findingsByRule(report)["collision"]
	if len(coll) != 1 {
		t.Fatalf("expected 1 collision finding, got %d: %+v", len(coll), coll)
	}
	if len(coll[0].Lines) != 2 || coll[0].Lines[0] != 1 || coll[0].Lines[1] != 2 {
		t.Errorf("collision lines = %v, want [1 2]", coll[0].Lines)
	}
	if coll[0].Severity != SeverityWarning {
		t.Errorf("collision severity = %v, want warning", coll[0].Severity)
	}
}

func TestCollisionDedupedAcrossDays(t *testing.T) {
	// A daily collision must be reported once, not once per sampled day.
	in := "0 3 * * * /bin/a\n0 3 * * * /bin/b\n"
	report, _ := Lint(strings.NewReader(in), nil)
	coll := findingsByRule(report)["collision"]
	if len(coll) != 1 {
		t.Fatalf("daily collision should report once, got %d", len(coll))
	}
}

func TestNoCollisionWhenStartsDiffer(t *testing.T) {
	in := "0 3 * * * /bin/a\n5 3 * * * /bin/b\n"
	report, _ := Lint(strings.NewReader(in), nil)
	if coll := findingsByRule(report)["collision"]; len(coll) != 0 {
		t.Errorf("expected no collision for distinct minutes, got %+v", coll)
	}
}

func TestParseErrorBecomesErrorFinding(t *testing.T) {
	in := "0 3 * * * /bin/ok\n99 3 * * * /bin/bad\n"
	report, err := Lint(strings.NewReader(in), nil)
	if err != nil {
		t.Fatalf("Lint error: %v", err)
	}
	pe := findingsByRule(report)["parse-error"]
	if len(pe) != 1 {
		t.Fatalf("expected 1 parse-error finding, got %d", len(pe))
	}
	if pe[0].Severity != SeverityError {
		t.Errorf("parse-error severity = %v, want error", pe[0].Severity)
	}
	if pe[0].Lines[0] != 2 {
		t.Errorf("parse-error line = %v, want [2]", pe[0].Lines)
	}
	// The bad line should not count as a healthy schedule entry.
	if report.Entries != 1 {
		t.Errorf("entries = %d, want 1 (bad line excluded)", report.Entries)
	}
}

func TestReportWorstAndCounts(t *testing.T) {
	in := "* * * * * /bin/spin\n0 0 30 2 * /bin/never\n"
	report, _ := Lint(strings.NewReader(in), nil)
	if report.Worst() != SeverityError {
		t.Errorf("Worst() = %v, want error", report.Worst())
	}
	info, warn, errs := report.Counts()
	if errs != 1 {
		t.Errorf("errors = %d, want 1", errs)
	}
	if warn < 1 {
		t.Errorf("warnings = %d, want >=1", warn)
	}
	_ = info
}

func TestCleanCrontabHasNoFindings(t *testing.T) {
	in := "0 3 * * * /bin/backup\n30 6 * * 1-5 /bin/report\n0 0 1 * * /bin/monthly\n"
	report, _ := Lint(strings.NewReader(in), nil)
	if len(report.Findings) != 0 {
		t.Errorf("expected clean crontab, got findings: %+v", report.Findings)
	}
	if report.Worst() != SeverityInfo {
		t.Errorf("Worst() = %v, want info", report.Worst())
	}
	if report.Entries != 3 {
		t.Errorf("entries = %d, want 3", report.Entries)
	}
}

func TestFindingsSortedByLine(t *testing.T) {
	// Dead expr on line 1, collision across 2&3; output must lead with line 1.
	in := "0 0 30 2 * /bin/never\n0 3 * * * /bin/a\n0 3 * * * /bin/b\n"
	report, _ := Lint(strings.NewReader(in), nil)
	if len(report.Findings) < 2 {
		t.Fatalf("expected >=2 findings, got %d", len(report.Findings))
	}
	if firstLine(report.Findings[0]) > firstLine(report.Findings[1]) {
		t.Errorf("findings not sorted by line: %+v", report.Findings)
	}
}

func TestSeverityString(t *testing.T) {
	if SeverityInfo.String() != "info" || SeverityWarning.String() != "warning" || SeverityError.String() != "error" {
		t.Errorf("severity strings wrong: %s/%s/%s",
			SeverityInfo, SeverityWarning, SeverityError)
	}
}
