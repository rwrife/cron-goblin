package lintrules

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rwrife/cron-goblin/internal/lint"
	"github.com/rwrife/cron-goblin/internal/parse"
)

// entry is a small helper: parse a cron expr into a lint.Entry on a given line.
func entry(t *testing.T, line int, expr, cmd string) lint.Entry {
	t.Helper()
	sched, err := parse.Parse(expr)
	if err != nil {
		t.Fatalf("parse %q: %v", expr, err)
	}
	return lint.Entry{Line: line, Raw: expr + " " + cmd, Schedule: sched, Command: cmd}
}

// writeRules writes a rules dir with the given filename->content and returns it.
func writeRules(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

func TestLoadDir_ForbiddenWindowFires(t *testing.T) {
	dir := writeRules(t, map[string]string{
		"policy.toml": `
[[rule]]
name = "no-backup-window"
severity = "error"
message = "jobs must not fire during the 02:00-04:00 backup window"
forbid_hours = [2, 3]
`,
	})

	rules, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("want 1 rule, got %d", len(rules))
	}
	if rules[0].Name() != "no-backup-window" {
		t.Fatalf("unexpected rule name %q", rules[0].Name())
	}

	entries := []lint.Entry{
		entry(t, 1, "0 3 * * *", "/backup-collides"), // 03:00 → in window
		entry(t, 2, "0 9 * * *", "/morning-report"),  // 09:00 → fine
	}
	findings := rules[0].Check(entries)
	if len(findings) != 1 {
		t.Fatalf("want 1 finding, got %d: %+v", len(findings), findings)
	}
	f := findings[0]
	if f.Rule != "no-backup-window" {
		t.Errorf("finding rule = %q", f.Rule)
	}
	if f.Severity != lint.SeverityError {
		t.Errorf("finding severity = %v, want error", f.Severity)
	}
	if len(f.Lines) != 1 || f.Lines[0] != 1 {
		t.Errorf("finding lines = %v, want [1]", f.Lines)
	}
}

func TestLoadDir_FieldMatch(t *testing.T) {
	dir := writeRules(t, map[string]string{
		"fields.toml": `
[[rule]]
name = "no-weekend-only-marker"
message = "flagged: minute 0 on Sundays"
fields = { minute = "0", dow = "0" }
`,
	})
	rules, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	entries := []lint.Entry{
		entry(t, 1, "0 12 * * 0", "/sunday"),  // matches
		entry(t, 2, "0 12 * * 1", "/monday"),  // dow differs
		entry(t, 3, "5 12 * * 0", "/off-min"), // minute differs
	}
	findings := rules[0].Check(entries)
	if len(findings) != 1 || findings[0].Lines[0] != 1 {
		t.Fatalf("want single finding on line 1, got %+v", findings)
	}
}

func TestLoadDir_MaxPerHour(t *testing.T) {
	dir := writeRules(t, map[string]string{
		"cadence.toml": `
[[rule]]
name = "hourly-budget"
message = "fires more than 4 times in an hour"
max_per_hour = 4
`,
	})
	rules, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	entries := []lint.Entry{
		entry(t, 1, "*/10 * * * *", "/six-per-hour"), // 6/hour → over budget
		entry(t, 2, "0 * * * *", "/once-per-hour"),   // 1/hour → fine
	}
	findings := rules[0].Check(entries)
	if len(findings) != 1 || findings[0].Lines[0] != 1 {
		t.Fatalf("want single finding on line 1, got %+v", findings)
	}
}

func TestLoadDir_MalformedFailsLoudlyWithPath(t *testing.T) {
	dir := writeRules(t, map[string]string{
		"broken.toml": `this is not = valid = toml [[[`,
	})
	_, err := LoadDir(dir)
	if err == nil {
		t.Fatal("want error for malformed TOML, got nil")
	}
	path := filepath.Join(dir, "broken.toml")
	if !contains(err.Error(), path) {
		t.Errorf("error %q should name the offending path %q", err, path)
	}
}

func TestCompile_RejectsBadSpecs(t *testing.T) {
	cases := map[string]ruleSpec{
		"no name":       {Message: "m", ForbidHours: []int{2}},
		"no message":    {Name: "x", ForbidHours: []int{2}},
		"bad severity":  {Name: "x", Message: "m", Severity: "nope", ForbidHours: []int{2}},
		"hour range":    {Name: "x", Message: "m", ForbidHours: []int{25}},
		"unknown field": {Name: "x", Message: "m", Fields: map[string]string{"nope": "0"}},
		"no conditions": {Name: "x", Message: "m"},
	}
	for name, spec := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := compile(spec); err == nil {
				t.Fatalf("compile(%q) = nil error, want failure", name)
			}
		})
	}
}

func TestLoadDir_DuplicateNameRejected(t *testing.T) {
	dir := writeRules(t, map[string]string{
		"a.toml": "[[rule]]\nname=\"dup\"\nmessage=\"m\"\nforbid_hours=[2]\n",
		"b.toml": "[[rule]]\nname=\"dup\"\nmessage=\"m\"\nforbid_hours=[3]\n",
	})
	if _, err := LoadDir(dir); err == nil {
		t.Fatal("want duplicate-name error, got nil")
	}
}

func TestLoadDir_MissingDirIsEmpty(t *testing.T) {
	rules, err := LoadDir(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("missing dir should not error: %v", err)
	}
	if len(rules) != 0 {
		t.Fatalf("missing dir should yield no rules, got %d", len(rules))
	}
}

func TestLoadDir_UnknownKeyRejected(t *testing.T) {
	dir := writeRules(t, map[string]string{
		"typo.toml": "[[rule]]\nname=\"x\"\nmessage=\"m\"\nforbid_hours=[2]\nseverty=\"error\"\n",
	})
	if _, err := LoadDir(dir); err == nil {
		t.Fatal("want unknown-key error for 'severty' typo, got nil")
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
