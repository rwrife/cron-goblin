package explain

import (
	"strings"
	"testing"

	"github.com/rwrife/cron-goblin/internal/parse"
)

// TestExplainGolden locks in the human-readable rendering for a table of
// representative expressions. These strings are the contract: if phrasing
// changes, this test should change deliberately alongside it.
func TestExplainGolden(t *testing.T) {
	cases := []struct {
		expr string
		want string
	}{
		{"* * * * *", "Every minute every day"},
		{"*/5 * * * *", "Every 5 minutes every day"},
		{"0 * * * *", "At the top of every hour every day"},
		{"30 * * * *", "At 30 minutes past every hour every day"},
		{"0 0 * * *", "At 00:00 every day"},
		{"30 6 * * 1-5", "At 06:30 on weekdays (Monday through Friday)"},
		{"0 0 * * 0", "At 00:00 on Sunday"},
		{"5 4 * * sun", "At 04:05 on Sunday"},
		{"0 0 * * 6,0", "At 00:00 on weekends (Saturday and Sunday)"},
		{"0 9 1 * *", "At 09:00 on the 1st"},
		{"15 14 1,15 * *", "At 14:15 on the 1st and 15th"},
		{"0 0 1 1 *", "At 00:00 on the 1st in January"},
		{"0 0,12 * * *", "At 00:00 and 12:00 every day"},
		{"*/15 9-17 * * 1-5", "Every 15 minutes during the hours 09:00–17:00 on weekdays (Monday through Friday)"},
		{"0 0 13 * 5", "At 00:00 on the 13th or on Friday (whichever matches first)"},
		{"0 0 1 */3 *", "At 00:00 on the 1st in January, April, July and October"},
		{"0 12 * * 1-3", "At 12:00 on Monday through Wednesday"},
	}

	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			sched, err := parse.Parse(tc.expr)
			if err != nil {
				t.Fatalf("Parse(%q) error: %v", tc.expr, err)
			}
			got := Explain(sched)
			if got != tc.want {
				t.Errorf("Explain(%q):\n  got:  %q\n  want: %q", tc.expr, got, tc.want)
			}
		})
	}
}

// TestExplainShape checks invariants that hold for every explanation: it is
// non-empty, capitalized, and carries no trailing period (so callers compose
// freely).
func TestExplainShape(t *testing.T) {
	exprs := []string{
		"* * * * *", "0 0 * * *", "*/7 1-4 * * *", "0 0 29 2 *",
		"0 0 * jan-dec mon-fri", "30 6 1,15 6 *", "0 */2 * * *",
	}
	for _, e := range exprs {
		sched, err := parse.Parse(e)
		if err != nil {
			t.Fatalf("Parse(%q) error: %v", e, err)
		}
		got := Explain(sched)
		if got == "" {
			t.Errorf("Explain(%q) returned empty string", e)
			continue
		}
		first := got[:1]
		if first != strings.ToUpper(first) {
			t.Errorf("Explain(%q) not capitalized: %q", e, got)
		}
		if strings.HasSuffix(got, ".") {
			t.Errorf("Explain(%q) should not end with a period: %q", e, got)
		}
	}
}

// TestExplainDeterministic ensures stable output for identical input.
func TestExplainDeterministic(t *testing.T) {
	sched, err := parse.Parse("*/15 9-17 * * 1-5")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if Explain(sched) != Explain(sched) {
		t.Fatal("Explain is not deterministic for the same schedule")
	}
}

// TestExplainDOWUnionMentioned makes the cron OR-rule explicit: when both
// day-of-month and day-of-week are restricted, the explanation must flag it.
func TestExplainDOWUnionMentioned(t *testing.T) {
	sched, err := parse.Parse("0 0 13 * 5")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	got := Explain(sched)
	if !strings.Contains(got, "or") || !strings.Contains(got, "whichever") {
		t.Errorf("expected OR-rule wording for DOM+DOW restriction, got: %q", got)
	}
}

// TestOrdinal spot-checks the ordinal helper across tricky boundaries.
func TestOrdinal(t *testing.T) {
	cases := map[int]string{
		1: "1st", 2: "2nd", 3: "3rd", 4: "4th",
		11: "11th", 12: "12th", 13: "13th",
		21: "21st", 22: "22nd", 23: "23rd", 31: "31st",
	}
	for n, want := range cases {
		if got := ordinal(n); got != want {
			t.Errorf("ordinal(%d) = %q, want %q", n, got, want)
		}
	}
}
