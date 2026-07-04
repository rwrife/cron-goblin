package dialect

import (
	"errors"
	"testing"

	"github.com/rwrife/cron-goblin/internal/parse"
)

// TestFromSystemdGolden locks in the accepted systemd OnCalendar -> standard-cron
// mappings. Every "want" here is also re-parsed by parse.Parse in
// TestFromSystemdProducesValidCron to prove the output is real standard cron.
func TestFromSystemdGolden(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// Named shorthands.
		{"minutely", "minutely", "* * * * *"},
		{"hourly", "hourly", "0 * * * *"},
		{"daily", "daily", "0 0 * * *"},
		{"weekly", "weekly", "0 0 * * MON"},
		{"monthly", "monthly", "0 0 1 * *"},
		{"quarterly", "quarterly", "0 0 1 1,4,7,10 *"},
		{"yearly", "yearly", "0 0 1 1 *"},
		{"annually", "annually", "0 0 1 1 *"},
		{"semiannually", "semiannually", "0 0 1 1,7 *"},
		{"case-insensitive shorthand", "Daily", "0 0 * * *"},

		// Time-only components (date/dow default to any/any; systemd time-only
		// still means "that time, every day").
		{"time only HH:MM", "09:00", "0 9 * * *"},
		{"time only with seconds zero", "12:30:00", "30 12 * * *"},
		{"every 15 minutes", "*:0/15", "0/15 * * * *"},
		{"top of every hour via star minute list", "*:00", "0 * * * *"},

		// Date components.
		{"first of month", "*-*-01 00:00", "0 0 1 * *"},
		{"specific month-day", "*-07-04 12:00", "0 12 4 7 *"},
		{"month list on the first", "*-01,07-01 00:00", "0 0 1 1,7 *"},
		{"year wildcard is fine", "*-*-15 06:30", "30 6 15 * *"},
		{"M-D form (year omitted)", "12-25 08:00", "0 8 25 12 *"},

		// Weekday components.
		{"single weekday", "Mon", "0 0 * * MON"},
		{"weekday with time", "Mon 09:00", "0 9 * * MON"},
		{"weekday list", "Mon,Wed,Fri 08:30", "30 8 * * MON,WED,FRI"},
		{"weekday range expands", "Mon..Fri 09:00", "0 9 * * MON,TUE,WED,THU,FRI"},
		{"weekday range wrapping in cron nums", "Fri..Sun 12:00", "0 12 * * FRI,SAT,SUN"},
		{"long weekday names", "Saturday,Sunday 10:00", "0 10 * * SAT,SUN"},
		{"numeric weekday (systemd 1-7)", "1..5 09:00", "0 9 * * MON,TUE,WED,THU,FRI"},

		// Full three-component expression.
		{"dow date time", "Mon *-*-01 00:00", "0 0 1 * MON"},
	}

	for _, c := range cases {
		got, err := FromSystemd(c.in)
		if err != nil {
			t.Errorf("%s: FromSystemd(%q) unexpected error: %v", c.name, c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: FromSystemd(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// TestFromSystemdProducesValidCron proves every accepted conversion yields a
// string the trusted parser accepts — the whole point of the converter is to
// feed the rest of the pipeline.
func TestFromSystemdProducesValidCron(t *testing.T) {
	inputs := []string{
		"minutely", "hourly", "daily", "weekly", "monthly",
		"quarterly", "yearly", "semiannually",
		"09:00", "12:30:00", "*:0/15", "*:00",
		"*-*-01 00:00", "*-07-04 12:00", "*-01,07-01 00:00", "12-25 08:00",
		"Mon", "Mon 09:00", "Mon,Wed,Fri 08:30", "Mon..Fri 09:00",
		"Fri..Sun 12:00", "Saturday,Sunday 10:00", "1..5 09:00",
		"Mon *-*-01 00:00",
	}
	for _, in := range inputs {
		out, err := FromSystemd(in)
		if err != nil {
			t.Errorf("FromSystemd(%q) unexpected error: %v", in, err)
			continue
		}
		if _, perr := parse.Parse(out); perr != nil {
			t.Errorf("FromSystemd(%q) = %q which parse.Parse rejected: %v", in, out, perr)
		}
	}
}

// TestFromSystemdErrors covers the honest refusals: malformed shapes, sub-minute
// precision, specific years, systemd-only specials, and typo'd shorthands. It
// also spot-checks the Lossy flag so callers can distinguish "impossible" from
// "lossy" (a valid systemd expression cron simply cannot carry).
func TestFromSystemdErrors(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		wantLossy bool
	}{
		{"empty", "", false},
		{"unknown shorthand", "notaword", false},
		{"typoed shorthand", "daly", false},
		{"nonzero seconds", "*-*-* 00:00:30", true},
		{"seconds via time-only", "12:00:30", true},
		{"specific year", "2027-01-01 00:00", true},
		{"year in three-field date", "2030-06-15 09:00", true},
		{"tilde last-day marker", "*-*-~3 00:00", false},
		{"bad time shape", "1:2:3:4", false},
		{"non-numeric hour", "ab:00", false},
		{"weekday out of range", "8 09:00", false},
		{"inverted weekday range", "Fri..Mon 09:00", false},
		{"bad weekday name", "Funday 09:00", false},
	}

	for _, c := range cases {
		_, err := FromSystemd(c.in)
		if err == nil {
			t.Errorf("%s: FromSystemd(%q) expected an error, got nil", c.name, c.in)
			continue
		}
		var ce *ConvertError
		if !errors.As(err, &ce) {
			t.Errorf("%s: FromSystemd(%q) error is %T, want *ConvertError", c.name, c.in, err)
			continue
		}
		if ce.Lossy != c.wantLossy {
			t.Errorf("%s: FromSystemd(%q) Lossy = %v, want %v (err: %v)", c.name, c.in, ce.Lossy, c.wantLossy, err)
		}
	}
}

// TestFromSystemdErrorMentionsField makes sure the targeted refusals name the
// offending field, since that context is the reason to refuse instead of guess.
func TestFromSystemdErrorMentionsField(t *testing.T) {
	cases := []struct {
		in    string
		field string
	}{
		{"*-*-* 00:00:30", "seconds"},
		{"2027-01-01 00:00", "year"},
		{"*-*-~3 00:00", "day-of-month"},
		{"Funday 09:00", "day-of-week"},
		{"ab:00", "hour"},
	}
	for _, c := range cases {
		_, err := FromSystemd(c.in)
		if err == nil {
			t.Fatalf("FromSystemd(%q) expected error", c.in)
		}
		var ce *ConvertError
		if !errors.As(err, &ce) {
			t.Fatalf("FromSystemd(%q) error is %T, want *ConvertError", c.in, err)
		}
		if ce.Field != c.field {
			t.Errorf("FromSystemd(%q) Field = %q, want %q", c.in, ce.Field, c.field)
		}
	}
}

// TestFromSystemdDialectPrefix checks that ConvertError from the systemd path is
// tagged with the systemd dialect (its Error() string leads with it), so the CLI
// diagnostics are unambiguous about which converter refused.
func TestFromSystemdDialectPrefix(t *testing.T) {
	_, err := FromSystemd("2027-01-01 00:00")
	if err == nil {
		t.Fatal("expected error for a specific year")
	}
	var ce *ConvertError
	if !errors.As(err, &ce) {
		t.Fatalf("error is %T, want *ConvertError", err)
	}
	if ce.Dialect != Systemd {
		t.Errorf("Dialect = %q, want %q", ce.Dialect, Systemd)
	}
}
