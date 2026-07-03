package dialect

import (
	"errors"
	"testing"

	"github.com/rwrife/cron-goblin/internal/parse"
)

// TestParseDialect covers the name/alias resolution and the unknown-name error.
func TestParseDialect(t *testing.T) {
	cases := []struct {
		in   string
		want Dialect
	}{
		{"cron", Cron},
		{"Cron", Cron},
		{"unix", Cron},
		{"standard", Cron},
		{"quartz", Quartz},
		{"QUARTZ", Quartz},
		{"java", Quartz},
		{"k8s", K8s},
		{"kubernetes", K8s},
		{"cronjob", K8s},
		{"systemd", Systemd},
		{"oncalendar", Systemd},
		{"  quartz  ", Quartz},
	}
	for _, c := range cases {
		got, err := ParseDialect(c.in)
		if err != nil {
			t.Errorf("ParseDialect(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseDialect(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	if _, err := ParseDialect("nonsense"); err == nil {
		t.Errorf("ParseDialect(nonsense) expected an error, got nil")
	}
}

// TestFromQuartzGolden locks in the accepted Quartz -> standard-cron mappings.
// Every "want" here is also re-parsed by parse.Parse in
// TestFromQuartzProducesValidCron to prove the output is real standard cron.
func TestFromQuartzGolden(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"every minute", "0 * * * * ?", "* * * * *"},
		{"top of every hour", "0 0 * * * ?", "0 * * * *"},
		{"daily 2:30am", "0 30 2 * * ?", "30 2 * * *"},
		{"question mark in DOM instead", "0 0 12 ? * *", "0 12 * * *"},
		// Quartz MON-FRI numerically is 2-6; standard cron is 1-5.
		{"weekdays numeric", "0 0 9 ? * 2-6", "0 9 * * 1-5"},
		{"weekdays named", "0 0 9 ? * MON-FRI", "0 9 * * MON-FRI"},
		// Quartz Sunday is 1; standard cron Sunday is 0.
		{"sunday numeric", "0 0 8 ? * 1", "0 8 * * 0"},
		{"saturday numeric", "0 0 8 ? * 7", "0 8 * * 6"},
		{"weekday list numeric", "0 15 10 ? * 2,4,6", "15 10 * * 1,3,5"},
		{"named list passthrough", "0 15 10 ? * MON,WED,FRI", "15 10 * * MON,WED,FRI"},
		{"day of month specific", "0 0 0 1 * ?", "0 0 1 * *"},
		{"month named", "0 0 0 1 JAN ?", "0 0 1 JAN *"},
		{"seven-field wildcard year", "0 0 12 * * ? *", "0 12 * * *"},
		{"step on DOW numeric", "0 0 12 ? * 2/2", "0 12 * * 1/2"},
		{"range with step numeric", "0 0 12 ? * 2-6/2", "0 12 * * 1-5/2"},
	}

	for _, c := range cases {
		got, err := FromQuartz(c.in)
		if err != nil {
			t.Errorf("%s: FromQuartz(%q) unexpected error: %v", c.name, c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: FromQuartz(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// TestFromQuartzProducesValidCron proves every accepted conversion yields a
// string the trusted parser accepts — the whole point of the converter is to
// feed the rest of the pipeline.
func TestFromQuartzProducesValidCron(t *testing.T) {
	inputs := []string{
		"0 * * * * ?",
		"0 0 * * * ?",
		"0 30 2 * * ?",
		"0 0 12 ? * *",
		"0 0 9 ? * 2-6",
		"0 0 9 ? * MON-FRI",
		"0 0 8 ? * 1",
		"0 0 8 ? * 7",
		"0 15 10 ? * 2,4,6",
		"0 0 0 1 * ?",
		"0 0 0 1 JAN ?",
		"0 0 12 * * ? *",
		"0 0 12 ? * 2/2",
		"0 0 12 ? * 2-6/2",
	}
	for _, in := range inputs {
		out, err := FromQuartz(in)
		if err != nil {
			t.Errorf("FromQuartz(%q) unexpected error: %v", in, err)
			continue
		}
		if _, perr := parse.Parse(out); perr != nil {
			t.Errorf("FromQuartz(%q) = %q which parse.Parse rejected: %v", in, out, perr)
		}
	}
}

// TestFromQuartzErrors covers the honest refusals: malformed shapes, sub-minute
// precision, specific years, and Quartz-only special characters. It also spot-
// checks the Lossy flag so callers can distinguish "impossible" from "lossy".
func TestFromQuartzErrors(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		wantLossy bool
	}{
		{"empty", "", false},
		{"too few fields", "0 0 12 * *", false},
		{"too many fields", "0 0 12 * * ? * *", false},
		{"nonzero seconds", "30 0 12 * * ?", true},
		{"wildcard seconds", "* 0 12 * * ?", true},
		{"seconds range", "0-30 0 12 * * ?", true},
		{"specific year", "0 0 12 * * ? 2027", true},
		{"year range", "0 0 12 * * ? 2027-2030", true},
		{"L last day of month", "0 0 12 L * ?", false},
		{"L in day of week", "0 0 12 ? * 6L", false},
		{"W nearest weekday", "0 0 12 15W * ?", false},
		{"hash nth weekday", "0 0 12 ? * 6#3", false},
		{"dow out of range high", "0 0 12 ? * 8", false},
		{"dow not a number", "0 0 12 ? * FUN", false},
	}

	for _, c := range cases {
		_, err := FromQuartz(c.in)
		if err == nil {
			t.Errorf("%s: FromQuartz(%q) expected an error, got nil", c.name, c.in)
			continue
		}
		var ce *ConvertError
		if !errors.As(err, &ce) {
			t.Errorf("%s: FromQuartz(%q) error is %T, want *ConvertError", c.name, c.in, err)
			continue
		}
		if ce.Lossy != c.wantLossy {
			t.Errorf("%s: FromQuartz(%q) Lossy = %v, want %v (err: %v)", c.name, c.in, ce.Lossy, c.wantLossy, err)
		}
	}
}

// TestFromQuartzErrorMentionsField makes sure the targeted refusals name the
// offending field, since that context is the reason to refuse instead of guess.
func TestFromQuartzErrorMentionsField(t *testing.T) {
	cases := []struct {
		in    string
		field string
	}{
		{"30 0 12 * * ?", "seconds"},
		{"0 0 12 * * ? 2027", "year"},
		{"0 0 12 L * ?", "day-of-month"},
		{"0 0 12 ? * 6#3", "day-of-week"},
	}
	for _, c := range cases {
		_, err := FromQuartz(c.in)
		if err == nil {
			t.Fatalf("FromQuartz(%q) expected error", c.in)
		}
		var ce *ConvertError
		if !errors.As(err, &ce) {
			t.Fatalf("FromQuartz(%q) error is %T, want *ConvertError", c.in, err)
		}
		if ce.Field != c.field {
			t.Errorf("FromQuartz(%q) Field = %q, want %q", c.in, ce.Field, c.field)
		}
	}
}
