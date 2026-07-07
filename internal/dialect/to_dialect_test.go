package dialect

import (
	"errors"
	"testing"

	"github.com/rwrife/cron-goblin/internal/parse"
)

// TestToQuartzGolden locks in the accepted standard-cron -> Quartz mappings.
// Every input here is a real 5-field cron expression (validated by parse.Parse
// in TestToQuartzInputsAreValidCron), and every "want" is the 6-field Quartz
// spelling with seconds prepended, `?` in the unused day field, and weekdays
// renumbered 0-6 (SUN-SAT) -> 1-7 (SUN-SAT).
func TestToQuartzGolden(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"every minute", "* * * * *", "0 * * * * ?"},
		{"top of every hour", "0 * * * *", "0 0 * * * ?"},
		{"daily 2:30am", "30 2 * * *", "0 30 2 * * ?"},
		{"day-of-month specific blanks DOW", "0 12 1 * *", "0 0 12 1 * ?"},
		// Restricting only day-of-week must blank day-of-month to `?`.
		{"weekdays named", "0 9 * * MON-FRI", "0 0 9 ? * MON-FRI"},
		// cron MON-FRI numerically is 1-5; Quartz is 2-6.
		{"weekdays numeric", "0 9 * * 1-5", "0 0 9 ? * 2-6"},
		// cron Sunday is 0; Quartz Sunday is 1.
		{"sunday numeric", "0 8 * * 0", "0 0 8 ? * 1"},
		// cron Saturday is 6; Quartz Saturday is 7.
		{"saturday numeric", "0 8 * * 6", "0 0 8 ? * 7"},
		// cron's 7 alias for Sunday maps to Quartz Sunday (1).
		{"sunday-7 alias", "0 8 * * 7", "0 0 8 ? * 1"},
		{"weekday list numeric", "15 10 * * 1,3,5", "0 15 10 ? * 2,4,6"},
		{"named list passthrough", "15 10 * * MON,WED,FRI", "0 15 10 ? * MON,WED,FRI"},
		{"month named", "0 0 1 JAN *", "0 0 0 1 JAN ?"},
		{"step on DOW numeric", "0 12 * * 1/2", "0 0 12 ? * 2/2"},
		{"range with step numeric", "0 12 * * 1-5/2", "0 0 12 ? * 2-6/2"},
		{"stepped minutes", "*/20 * * * *", "0 */20 * * * ?"},
	}

	for _, c := range cases {
		got, err := ToQuartz(c.in)
		if err != nil {
			t.Errorf("%s: ToQuartz(%q) unexpected error: %v", c.name, c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: ToQuartz(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// TestToQuartzInputsAreValidCron proves every ToQuartzGolden input is genuine
// standard cron (so the "want" mappings are grounded in real expressions, not
// hand-typed shapes the parser would reject).
func TestToQuartzInputsAreValidCron(t *testing.T) {
	inputs := []string{
		"* * * * *", "0 * * * *", "30 2 * * *", "0 12 1 * *",
		"0 9 * * MON-FRI", "0 9 * * 1-5", "0 8 * * 0", "0 8 * * 6",
		"0 8 * * 7", "15 10 * * 1,3,5", "15 10 * * MON,WED,FRI",
		"0 0 1 JAN *", "0 12 * * 1/2", "0 12 * * 1-5/2", "*/20 * * * *",
	}
	for _, in := range inputs {
		if _, err := parse.Parse(in); err != nil {
			t.Errorf("test input %q is not valid standard cron: %v", in, err)
		}
	}
}

// TestToQuartzRoundTrip is the property that makes the "Quartz <-> cron" claim
// real: for expressions representable in both dialects, ToQuartz then FromQuartz
// returns the original normalized cron. It guards against the two dialects
// drifting (especially the weekday numbering shift going the wrong way).
func TestToQuartzRoundTrip(t *testing.T) {
	crons := []string{
		"* * * * *",
		"0 * * * *",
		"30 2 * * *",
		"0 9 * * 1-5",
		"0 9 * * MON-FRI",
		"0 8 * * 0",
		"0 8 * * 6",
		"15 10 * * 1,3,5",
		"0 12 1 * *",
		"0 0 1 JAN *",
		"0 12 * * 1-5/2",
	}
	for _, c := range crons {
		q, err := ToQuartz(c)
		if err != nil {
			t.Errorf("ToQuartz(%q) unexpected error: %v", c, err)
			continue
		}
		back, err := FromQuartz(q)
		if err != nil {
			t.Errorf("FromQuartz(%q) [from ToQuartz(%q)] unexpected error: %v", q, c, err)
			continue
		}
		if back != c {
			t.Errorf("round-trip mismatch: %q -> ToQuartz %q -> FromQuartz %q, want original %q", c, q, back, c)
		}
	}
}

// TestToQuartzBothDayFieldsRefused verifies the single shape Quartz cannot
// express: a cron that restricts both day-of-month and day-of-week (an OR).
// It must be refused as a lossy conversion, with the offending field named.
func TestToQuartzBothDayFieldsRefused(t *testing.T) {
	_, err := ToQuartz("0 9 15 * 1-5")
	if err == nil {
		t.Fatal("expected a refusal when both day-of-month and day-of-week are restricted")
	}
	var ce *ConvertError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *ConvertError, got %T: %v", err, err)
	}
	if !ce.Lossy {
		t.Errorf("both-day-fields refusal should be marked Lossy, got Lossy=false: %v", ce)
	}
	if ce.Field == "" {
		t.Errorf("expected the offending field to be named, got empty Field: %v", ce)
	}
}

// TestToQuartzRejectsWrongFieldCount confirms ToQuartz only accepts 5-field
// input (it is fed normalized cron; anything else is a programming error worth
// surfacing).
func TestToQuartzRejectsWrongFieldCount(t *testing.T) {
	for _, in := range []string{"", "0 9 * *", "0 0 9 * * ?"} {
		if _, err := ToQuartz(in); err == nil {
			t.Errorf("ToQuartz(%q) expected an error for non-5-field input, got nil", in)
		}
	}
}

// TestToQuartzRejectsBadWeekday makes sure an out-of-range numeric weekday is
// reported rather than silently shifted into a wrong Quartz value.
func TestToQuartzRejectsBadWeekday(t *testing.T) {
	if _, err := ToQuartz("0 9 * * 9"); err == nil {
		t.Error("expected an error for an out-of-range cron weekday (9)")
	}
}

// TestToK8sGolden covers the standard-cron -> Kubernetes CronJob direction: a
// plain schedule passes through validated, and with macros enabled the canonical
// forms collapse to the robfig/cron `@`-macro a CronJob accepts.
func TestToK8sGolden(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		macros bool
		want   string
	}{
		{"passthrough no macros", "*/5 * * * *", false, "*/5 * * * *"},
		{"canonical daily without flag stays literal", "0 0 * * *", false, "0 0 * * *"},
		{"canonical daily collapses with flag", "0 0 * * *", true, "@daily"},
		{"canonical hourly collapses", "0 * * * *", true, "@hourly"},
		{"canonical weekly collapses", "0 0 * * 0", true, "@weekly"},
		{"canonical monthly collapses", "0 0 1 * *", true, "@monthly"},
		{"canonical yearly collapses", "0 0 1 1 *", true, "@yearly"},
		{"non-canonical stays literal even with flag", "5 0 * * *", true, "5 0 * * *"},
	}
	for _, c := range cases {
		got, err := ToK8s(c.in, c.macros)
		if err != nil {
			t.Errorf("%s: ToK8s(%q, %v) unexpected error: %v", c.name, c.in, c.macros, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: ToK8s(%q, %v) = %q, want %q", c.name, c.in, c.macros, got, c.want)
		}
	}
}

// TestToK8sMacrosRoundTrip proves the macro collapse is the inverse of FromK8s:
// a canonical cron collapses to a macro that FromK8s expands back to the same
// cron.
func TestToK8sMacrosRoundTrip(t *testing.T) {
	crons := []string{"0 0 1 1 *", "0 0 1 * *", "0 0 * * 0", "0 0 * * *", "0 * * * *"}
	for _, c := range crons {
		macro, err := ToK8s(c, true)
		if err != nil {
			t.Errorf("ToK8s(%q, true) unexpected error: %v", c, err)
			continue
		}
		back, err := FromK8s(macro)
		if err != nil {
			t.Errorf("FromK8s(%q) [from ToK8s(%q)] unexpected error: %v", macro, c, err)
			continue
		}
		if back != c {
			t.Errorf("k8s macro round-trip mismatch: %q -> %q -> %q, want %q", c, macro, back, c)
		}
	}
}

// TestToK8sRejectsEmpty guards the trivial empty-input case.
func TestToK8sRejectsEmpty(t *testing.T) {
	if _, err := ToK8s("   ", true); err == nil {
		t.Error("expected an error for an empty k8s schedule")
	}
}
