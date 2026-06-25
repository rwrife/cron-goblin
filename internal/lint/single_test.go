package lint

import (
	"strings"
	"testing"

	"github.com/rwrife/cron-goblin/internal/parse"
)

// mustParse parses an expression or fails the test.
func mustParse(t *testing.T, expr string) parse.Schedule {
	t.Helper()
	s, err := parse.Parse(expr)
	if err != nil {
		t.Fatalf("parse(%q): %v", expr, err)
	}
	return s
}

func TestCheckSchedule_DeadExpression(t *testing.T) {
	findings := CheckSchedule(mustParse(t, "0 0 30 2 *")) // Feb 30
	if len(findings) == 0 {
		t.Fatal("Feb 30 should produce a dead-expression finding")
	}
	var found bool
	for _, f := range findings {
		if f.Rule == "dead-expression" && f.Severity == SeverityError {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a dead-expression error finding, got %+v", findings)
	}
}

func TestCheckSchedule_TooFrequent(t *testing.T) {
	findings := CheckSchedule(mustParse(t, "* * * * *"))
	if len(findings) == 0 {
		t.Fatal("every-minute should produce a too-frequent finding")
	}
	if findings[0].Rule != "too-frequent" {
		t.Errorf("expected too-frequent, got %q", findings[0].Rule)
	}
}

func TestCheckSchedule_CleanExpressionHasNoFindings(t *testing.T) {
	// Every weekday at 09:00 — sane, single job, nothing to flag.
	findings := CheckSchedule(mustParse(t, "0 9 * * 1-5"))
	if len(findings) != 0 {
		t.Errorf("clean schedule should have no findings, got %+v", findings)
	}
}

func TestCheckSchedule_ExcludesCollision(t *testing.T) {
	// Collision needs >1 job; a single expression must never produce one even
	// if it would collide with a hypothetical sibling.
	findings := CheckSchedule(mustParse(t, "0 3 * * *"))
	for _, f := range findings {
		if f.Rule == "collision" {
			t.Errorf("single-schedule check must not emit collision findings: %+v", f)
		}
	}
}

func TestMessages_ExtractsText(t *testing.T) {
	findings := []Finding{
		{Rule: "a", Message: "first"},
		{Rule: "b", Message: "second"},
	}
	msgs := Messages(findings)
	if len(msgs) != 2 || msgs[0] != "first" || msgs[1] != "second" {
		t.Errorf("Messages = %v, want [first second]", msgs)
	}
	if !strings.Contains(strings.Join(Messages(nil), ""), "") {
		t.Error("Messages(nil) should be empty-safe")
	}
}
