package english

import (
	"strings"
	"testing"

	"github.com/rwrife/cron-goblin/internal/parse"
)

// TestParseGolden locks in the English -> cron mapping for the phrases M6
// promises to support. These are the contract: changing phrasing here is a
// deliberate behavior change.
func TestParseGolden(t *testing.T) {
	cases := []struct {
		phrase string
		want   string
	}{
		// The headline acceptance criteria from issue #6 / PLAN.md.
		{"every 15 minutes", "*/15 * * * *"},
		{"every weekday at 6:30pm", "30 18 * * 1-5"},

		// Periods.
		{"every minute", "* * * * *"},
		{"every 5 minutes", "*/5 * * * *"},
		{"every 30 minutes", "*/30 * * * *"},
		{"every hour", "0 * * * *"},
		{"hourly", "0 * * * *"},
		{"every 2 hours", "0 */2 * * *"},
		{"every 6 hours", "0 */6 * * *"},

		// Daily times, 12h and 24h.
		{"every day at 9am", "0 9 * * *"},
		{"daily at 6:30pm", "30 18 * * *"},
		{"at 9am", "0 9 * * *"},
		{"at midnight", "0 0 * * *"},
		{"at noon", "0 12 * * *"},
		{"every day at 14:30", "30 14 * * *"},
		{"every day at 12am", "0 0 * * *"},
		{"every day at 12pm", "0 12 * * *"},
		{"daily at 5:05am", "5 5 * * *"},

		// Weekday scoping.
		{"every weekday at 9am", "0 9 * * 1-5"},
		{"weekdays at 8:30am", "30 8 * * 1-5"},
		{"every weekend at noon", "0 12 * * 0,6"},
		{"weekends at midnight", "0 0 * * 0,6"},
		{"every monday at 8am", "0 8 * * 1"},
		{"every monday", "0 0 * * 1"},
		{"mondays at 8am", "0 8 * * 1"},
		{"every tuesday and thursday at 5pm", "0 17 * * 2,4"},
		{"mon, wed and fri at 7am", "0 7 * * 1,3,5"},
		{"saturday & sunday at 10am", "0 10 * * 6,0"},

		// Day-of-month scoping.
		{"first of the month at 9am", "0 9 1 * *"},
		{"on the 15th at noon", "0 12 15 * *"},
		{"the 1st at midnight", "0 0 1 * *"},
		{"monthly at 9am", "0 9 1 * *"},
		{"every month", "0 0 1 * *"},

		// Months.
		{"every january at midnight", "0 0 1 1 *"},
		{"every december at noon", "0 12 1 12 *"},

		// Named times-of-day (conventions locked here).
		{"every morning", "0 6 * * *"},
		{"every night", "0 21 * * *"},
		{"every evening", "0 18 * * *"},
		{"every afternoon", "0 12 * * *"},
		{"in the morning", "0 6 * * *"},
		{"at night", "0 21 * * *"},
		{"every weekday morning", "0 6 * * 1-5"},
		{"every weekday evening", "0 18 * * 1-5"},
		{"weekends at night", "0 21 * * 0,6"},
		{"every monday morning", "0 6 * * 1"},

		// Count-per-period.
		{"once a day", "0 0 * * *"},
		{"twice a day", "0 0,12 * * *"},
		{"three times a day", "0 0,8,16 * * *"},
		{"4 times a day", "0 0,6,12,18 * * *"},
		{"once an hour", "0 * * * *"},
		{"twice an hour", "0,30 * * * *"},

		// Multi-day / multi-month intervals.
		{"every 3 days", "0 0 */3 * *"},
		{"every other day", "0 0 */2 * *"},
		{"every 1 day", "0 0 * * *"},
		{"every other month", "0 0 1 */2 *"},
		{"every 3 months", "0 0 1 */3 *"},
		{"every other hour", "0 */2 * * *"},

		// Calendar cadences.
		{"quarterly", "0 0 1 1,4,7,10 *"},
		{"every quarter", "0 0 1 1,4,7,10 *"},
		{"yearly", "0 0 1 1 *"},
		{"annually", "0 0 1 1 *"},
		{"every year", "0 0 1 1 *"},

		// Lists of times (shared minute).
		{"every day at 9am and 5pm", "0 9,17 * * *"},
		{"at 9am and 5pm", "0 9,17 * * *"},
		{"every day at 9am, noon and 5pm", "0 9,12,17 * * *"},
		{"weekdays at 8am and 6pm", "0 8,18 * * 1-5"},
	}

	for _, tc := range cases {
		t.Run(tc.phrase, func(t *testing.T) {
			got, err := Parse(tc.phrase)
			if err != nil {
				t.Fatalf("Parse(%q) error: %v", tc.phrase, err)
			}
			if got != tc.want {
				t.Errorf("Parse(%q):\n  got:  %q\n  want: %q", tc.phrase, got, tc.want)
			}
		})
	}
}

// TestParseProducesValidCron guarantees that every phrase we accept yields a
// cron string the trusted parser also accepts. If english can emit something
// parse rejects, that's a bug in english.
func TestParseProducesValidCron(t *testing.T) {
	phrases := []string{
		"every minute", "every 15 minutes", "every 2 hours", "every hour",
		"every day at 9am", "daily at 6:30pm", "every weekday at 6:30pm",
		"weekends at noon", "every monday at 8am",
		"every tuesday and thursday at 5pm", "first of the month at 9am",
		"on the 15th at noon", "every january at midnight", "at midnight",
		"monthly at 9am", "mon, wed and fri at 7am",
		"every morning", "every weekday evening", "weekends at night",
		"once a day", "twice a day", "once an hour", "twice an hour",
		"every 3 days", "every other day", "every 3 months", "every other month",
		"quarterly", "yearly", "every day at 9am and 5pm",
	}
	for _, p := range phrases {
		expr, err := Parse(p)
		if err != nil {
			t.Fatalf("Parse(%q) error: %v", p, err)
		}
		if _, perr := parse.Parse(expr); perr != nil {
			t.Errorf("Parse(%q) produced %q which parse.Parse rejects: %v", p, expr, perr)
		}
	}
}

// TestParseCaseAndWhitespaceInsensitive verifies normalization handles messy
// real-world input.
func TestParseCaseAndWhitespaceInsensitive(t *testing.T) {
	cases := []struct {
		phrase string
		want   string
	}{
		{"Every 15 Minutes", "*/15 * * * *"},
		{"  EVERY   DAY   AT   9AM  ", "0 9 * * *"},
		{"Every Weekday At 6:30PM.", "30 18 * * 1-5"},
		{"Daily at 9am!", "0 9 * * *"},
	}
	for _, tc := range cases {
		got, err := Parse(tc.phrase)
		if err != nil {
			t.Fatalf("Parse(%q) error: %v", tc.phrase, err)
		}
		if got != tc.want {
			t.Errorf("Parse(%q) = %q, want %q", tc.phrase, got, tc.want)
		}
	}
}

// TestParseErrors checks that unsupported or contradictory phrases fail loudly
// rather than guessing.
func TestParseErrors(t *testing.T) {
	bad := []string{
		"",                               // empty
		"   ",                            // whitespace only
		"every blue moon",                // gibberish recurrence
		"at 25:00",                       // bad hour
		"at 9:99am",                      // bad minute
		"at 13am",                        // invalid 12-hour
		"every 5 minutes at 9am",         // sub-hour + fixed time conflict
		"every hour at 9am",              // hourly + fixed time conflict
		"every 90 minutes",               // exceeds 59
		"every 30 hours",                 // exceeds 23
		"last of the month at 9am",       // cron can't express "last"
		"on the 40th at noon",            // out-of-range day
		"flibbertigibbet",                // pure nonsense
		"every 2 weeks",                  // cron can't do a multi-week cadence
		"every other week",               // same, spelled differently
		"biweekly",                       // same
		"fortnightly",                    // same
		"every 40 days",                  // exceeds a month
		"every 13 months",                // exceeds a year
		"every day at 9:15am and 5:45pm", // list with mismatched minutes
	}
	for _, p := range bad {
		if got, err := Parse(p); err == nil {
			t.Errorf("Parse(%q) = %q, expected an error", p, got)
		}
	}
}

// TestErrorMentionsPhrase ensures the error type echoes the offending input,
// so the CLI/goblin can show users what confused the parser.
func TestErrorMentionsPhrase(t *testing.T) {
	_, err := Parse("every blue moon")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "blue moon") {
		t.Errorf("error %q should mention the phrase", err.Error())
	}
}
