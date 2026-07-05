package dialect

import (
	"errors"
	"strings"
	"testing"

	"github.com/rwrife/cron-goblin/internal/parse"
)

// TestFromK8sGolden locks in the accepted Kubernetes CronJob -> standard-cron
// mappings: the robfig/cron `@`-macros the apiserver understands, plus plain
// 5-field cron passing straight through. Every "want" is re-parsed by
// parse.Parse in TestFromK8sProducesValidCron to prove it is real cron.
func TestFromK8sGolden(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// robfig/cron macros the CronJob controller accepts.
		{"yearly", "@yearly", "0 0 1 1 *"},
		{"annually alias", "@annually", "0 0 1 1 *"},
		{"monthly", "@monthly", "0 0 1 * *"},
		{"weekly", "@weekly", "0 0 * * 0"},
		{"daily", "@daily", "0 0 * * *"},
		{"midnight alias", "@midnight", "0 0 * * *"},
		{"hourly", "@hourly", "0 * * * *"},
		{"case-insensitive macro", "@Daily", "0 0 * * *"},
		{"macro with surrounding space", "  @hourly  ", "0 * * * *"},

		// Plain 5-field cron is already a valid k8s schedule and passes through
		// untouched (convert.go normalizes whitespace via parse.Parse downstream).
		{"plain cron", "*/5 * * * *", "*/5 * * * *"},
		{"cron with names", "0 9 * * MON-FRI", "0 9 * * MON-FRI"},
		{"cron sunday zero", "0 0 * * 0", "0 0 * * 0"},
	}

	for _, c := range cases {
		got, err := FromK8s(c.in)
		if err != nil {
			t.Errorf("%s: FromK8s(%q) unexpected error: %v", c.name, c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: FromK8s(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// TestFromK8sProducesValidCron proves every accepted conversion yields a string
// the trusted parser accepts — the converter exists to feed the rest of the
// pipeline, so its output must be real standard cron.
func TestFromK8sProducesValidCron(t *testing.T) {
	inputs := []string{
		"@yearly", "@annually", "@monthly", "@weekly",
		"@daily", "@midnight", "@hourly",
		"*/5 * * * *", "0 9 * * MON-FRI", "0 0 * * 0",
	}
	for _, in := range inputs {
		out, err := FromK8s(in)
		if err != nil {
			t.Errorf("FromK8s(%q) unexpected error: %v", in, err)
			continue
		}
		if _, perr := parse.Parse(out); perr != nil {
			t.Errorf("FromK8s(%q) = %q which parse.Parse rejected: %v", in, out, perr)
		}
	}
}

// TestFromK8sErrors covers the honest refusals: the empty schedule, the
// vixie-only @reboot and robfig @every that a CronJob does not honor, unknown
// macros, and Quartz constructs (wrong field count or `?`/`L`/`W`/`#` specials)
// pasted into a manifest. None of these are lossy in the "valid but cron can't
// carry it" sense — they are simply not valid k8s schedules — so Lossy stays
// false throughout.
func TestFromK8sErrors(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"whitespace only", "   "},
		{"reboot", "@reboot"},
		{"reboot case-insensitive", "@Reboot"},
		{"every bare", "@every"},
		{"every with duration", "@every 5m"},
		{"unknown macro", "@fortnightly"},
		{"quartz six field", "0 0 0 * * *"},
		{"quartz seven field", "0 0 0 * * * 2027"},
		{"question mark special", "0 12 ? * MON"},
		{"L last special", "0 12 L * *"},
		{"W nearest-weekday special", "0 12 15W * *"},
		{"hash nth-weekday special", "0 12 * * 6#3"},
	}

	for _, c := range cases {
		_, err := FromK8s(c.in)
		if err == nil {
			t.Errorf("%s: FromK8s(%q) expected an error, got nil", c.name, c.in)
			continue
		}
		var ce *ConvertError
		if !errors.As(err, &ce) {
			t.Errorf("%s: FromK8s(%q) error is %T, want *ConvertError", c.name, c.in, err)
			continue
		}
		if ce.Lossy {
			t.Errorf("%s: FromK8s(%q) Lossy = true, want false (k8s refusals are not lossy)", c.name, c.in)
		}
		if ce.Dialect != K8s {
			t.Errorf("%s: FromK8s(%q) Dialect = %q, want %q", c.name, c.in, ce.Dialect, K8s)
		}
	}
}

// TestFromK8sRebootMessage checks the @reboot refusal explains *why* a CronJob
// rejects it (no boot event), since that context is the whole reason to refuse
// with a targeted message rather than a generic parse error.
func TestFromK8sRebootMessage(t *testing.T) {
	_, err := FromK8s("@reboot")
	if err == nil {
		t.Fatal("expected an error for @reboot")
	}
	msg := err.Error()
	if !strings.Contains(msg, "@reboot") || !strings.Contains(strings.ToLower(msg), "boot") {
		t.Errorf("@reboot error should name @reboot and mention the missing boot event, got: %q", msg)
	}
}

// TestFromK8sQuartzRedirect confirms the Quartz-construct refusals point the
// user at `--from quartz`, and that the day-of-month vs day-of-week field is
// named for the single-special cases so the diagnostic is actionable.
func TestFromK8sQuartzRedirect(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		field string // expected ConvertError.Field ("" = don't assert the field)
	}{
		{"six field count", "0 0 0 * * *", "schedule"},
		{"question mark in dom", "0 12 ? * MON", "day-of-month"},
		{"L in dom", "0 12 L * *", "day-of-month"},
		{"hash in dow", "0 12 * * 6#3", "day-of-week"},
	}
	for _, c := range cases {
		_, err := FromK8s(c.in)
		if err == nil {
			t.Fatalf("%s: FromK8s(%q) expected an error", c.name, c.in)
		}
		if !strings.Contains(err.Error(), "--from quartz") {
			t.Errorf("%s: FromK8s(%q) should redirect to --from quartz, got: %q", c.name, c.in, err.Error())
		}
		var ce *ConvertError
		if !errors.As(err, &ce) {
			t.Fatalf("%s: FromK8s(%q) error is %T, want *ConvertError", c.name, c.in, err)
		}
		if c.field != "" && ce.Field != c.field {
			t.Errorf("%s: FromK8s(%q) Field = %q, want %q", c.name, c.in, ce.Field, c.field)
		}
	}
}

// TestFromK8sWeekdayNamesNotSpecials guards the token-aware special detection:
// weekday names that contain the letters W (WED) or that could look L-ish must
// not be misread as Quartz `W`/`L` specials. These are ordinary k8s schedules
// and must pass through.
func TestFromK8sWeekdayNamesNotSpecials(t *testing.T) {
	ok := []string{
		"0 9 * * WED",
		"0 9 * * WED,FRI",
		"0 9 * * MON-FRI",
		"0 9 * * SAT,SUN",
	}
	for _, in := range ok {
		got, err := FromK8s(in)
		if err != nil {
			t.Errorf("FromK8s(%q) unexpected error (weekday name misread as special?): %v", in, err)
			continue
		}
		if got != in {
			t.Errorf("FromK8s(%q) = %q, want unchanged passthrough", in, got)
		}
	}
}
