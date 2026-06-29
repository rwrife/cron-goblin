package lint

import (
	"strings"
	"testing"
	"time"
)

// loadTZ loads an IANA location or skips the test when the system has no
// timezone database (rare on CI Linux, but keeps these tests from failing
// hard in a stripped environment rather than reporting a real defect).
func loadTZ(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("timezone database unavailable for %q: %v", name, err)
	}
	return loc
}

// findTransition returns the first transition of the given kind, or fails.
func findTransition(t *testing.T, trs []dstTransition, kind transitionKind) dstTransition {
	t.Helper()
	for _, tr := range trs {
		if tr.kind == kind {
			return tr
		}
	}
	t.Fatalf("no transition of kind %d in %+v", kind, trs)
	return dstTransition{}
}

func TestDSTTransitions_USEastern2026(t *testing.T) {
	loc := loadTZ(t, "America/New_York")
	trs := dstTransitions(loc, 2026)
	if len(trs) != 2 {
		t.Fatalf("expected 2 US transitions in 2026, got %d: %+v", len(trs), trs)
	}

	gap := findTransition(t, trs, kindGap)
	if got := gap.date.Format("2006-01-02"); got != "2026-03-08" {
		t.Errorf("spring-forward date = %s, want 2026-03-08", got)
	}
	// US spring-forward removes the 02:00–03:00 wall-clock hour.
	if gap.startMin != 120 || gap.endMin != 180 {
		t.Errorf("gap window = [%d,%d), want [120,180) (02:00–03:00)", gap.startMin, gap.endMin)
	}
	if gap.delta != time.Hour {
		t.Errorf("gap delta = %s, want 1h", gap.delta)
	}

	overlap := findTransition(t, trs, kindOverlap)
	if got := overlap.date.Format("2006-01-02"); got != "2026-11-01" {
		t.Errorf("fall-back date = %s, want 2026-11-01", got)
	}
	// US fall-back repeats the 01:00–02:00 wall-clock hour.
	if overlap.startMin != 60 || overlap.endMin != 120 {
		t.Errorf("overlap window = [%d,%d), want [60,120) (01:00–02:00)", overlap.startMin, overlap.endMin)
	}
}

func TestDSTTransitions_SouthernHemisphereInverted(t *testing.T) {
	// Sydney springs forward in October and falls back in April — the seasons
	// (and thus the transition kinds' months) are inverted vs the north.
	loc := loadTZ(t, "Australia/Sydney")
	trs := dstTransitions(loc, 2026)
	if len(trs) != 2 {
		t.Fatalf("expected 2 Sydney transitions in 2026, got %d: %+v", len(trs), trs)
	}
	gap := findTransition(t, trs, kindGap)
	if m := int(gap.date.Month()); m != int(time.October) {
		t.Errorf("Sydney spring-forward month = %d, want October", m)
	}
	overlap := findTransition(t, trs, kindOverlap)
	if m := int(overlap.date.Month()); m != int(time.April) {
		t.Errorf("Sydney fall-back month = %d, want April", m)
	}
}

func TestDSTTransitions_NoDSTZones(t *testing.T) {
	// UTC is special-cased to nil; India never observes DST.
	if trs := dstTransitions(time.UTC, 2026); len(trs) != 0 {
		t.Errorf("UTC should have no transitions, got %+v", trs)
	}
	if trs := dstTransitions(nil, 2026); len(trs) != 0 {
		t.Errorf("nil location should have no transitions, got %+v", trs)
	}
	kol := loadTZ(t, "Asia/Kolkata")
	if trs := dstTransitions(kol, 2026); len(trs) != 0 {
		t.Errorf("Asia/Kolkata observes no DST, got %+v", trs)
	}
}

func TestDSTTransition_Affects(t *testing.T) {
	tr := dstTransition{startMin: 120, endMin: 180} // 02:00–03:00
	cases := []struct {
		h, m int
		want bool
	}{
		{2, 0, true},   // 02:00 — inclusive lower bound
		{2, 30, true},  // 02:30 — squarely inside
		{2, 59, true},  // 02:59 — last affected minute
		{3, 0, false},  // 03:00 — exclusive upper bound
		{1, 59, false}, // 01:59 — just before
		{4, 0, false},  // unrelated
	}
	for _, c := range cases {
		if got := tr.affects(c.h, c.m); got != c.want {
			t.Errorf("affects(%02d:%02d) = %v, want %v", c.h, c.m, got, c.want)
		}
	}
}

// run the DST rule over a single expression in a location and return findings.
func dstFindings(t *testing.T, expr string, loc *time.Location) []Finding {
	t.Helper()
	// Anchor "now" in early 2026 so both 2026 transitions are in range.
	now := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	rule := newDSTDangerRule(loc, now)
	entry := Entry{Line: 1, Raw: mustParse(t, expr).Raw, Schedule: mustParse(t, expr)}
	return rule.Check([]Entry{entry})
}

func TestDSTDangerRule_FlagsSpringForwardGap(t *testing.T) {
	loc := loadTZ(t, "America/New_York")
	// 02:30 every day lands in the missing spring-forward hour → skipped.
	fs := dstFindings(t, "30 2 * * *", loc)
	var warn *Finding
	for i := range fs {
		if fs[i].Severity == SeverityWarning {
			warn = &fs[i]
		}
	}
	if warn == nil {
		t.Fatalf("expected a warning for a 02:30 daily job, got %+v", fs)
	}
	if warn.Rule != "dst-danger" {
		t.Errorf("rule = %q, want dst-danger", warn.Rule)
	}
	if !strings.Contains(warn.Message, "skipped") {
		t.Errorf("gap message should mention it is skipped: %q", warn.Message)
	}
}

func TestDSTDangerRule_FlagsFallBackOverlapAsInfo(t *testing.T) {
	loc := loadTZ(t, "America/New_York")
	// 01:30 daily lands in the repeated fall-back hour → ambiguous (info).
	fs := dstFindings(t, "30 1 * * *", loc)
	if len(fs) != 1 {
		t.Fatalf("expected exactly one finding for 01:30 daily, got %+v", fs)
	}
	if fs[0].Severity != SeverityInfo {
		t.Errorf("overlap severity = %v, want info", fs[0].Severity)
	}
	if !strings.Contains(fs[0].Message, "twice") {
		t.Errorf("overlap message should mention the time happens twice: %q", fs[0].Message)
	}
}

func TestDSTDangerRule_IgnoresSafeTimes(t *testing.T) {
	loc := loadTZ(t, "America/New_York")
	// 04:00 is clear of both US transition windows.
	if fs := dstFindings(t, "0 4 * * *", loc); len(fs) != 0 {
		t.Errorf("04:00 daily should not trip the DST rule, got %+v", fs)
	}
}

func TestDSTDangerRule_RespectsDayMatching(t *testing.T) {
	loc := loadTZ(t, "America/New_York")
	// 2026-03-08 (the spring-forward day) is a Sunday. A job pinned to 02:30 on
	// *weekdays only* never fires that Sunday, so the gap must NOT be flagged.
	if fs := dstFindings(t, "30 2 * * 1-5", loc); len(fs) != 0 {
		t.Errorf("weekday-only 02:30 should miss the Sunday gap, got %+v", fs)
	}
	// But a job restricted to Sundays at 02:30 DOES hit the gap.
	fs := dstFindings(t, "30 2 * * 0", loc)
	if len(fs) == 0 {
		t.Errorf("Sunday 02:30 should hit the spring-forward gap, got none")
	}
}

func TestDSTDangerRule_NoDSTLocationIsSilent(t *testing.T) {
	// Even a 02:30 daily job is fine in a zone that never shifts.
	if fs := dstFindings(t, "30 2 * * *", time.UTC); len(fs) != 0 {
		t.Errorf("UTC must produce no DST findings, got %+v", fs)
	}
}

func TestDSTDangerRule_SkipsUnparseableEntries(t *testing.T) {
	loc := loadTZ(t, "America/New_York")
	rule := newDSTDangerRule(loc, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	bad := Entry{Line: 1, ParseErr: &parseErrStub{}}
	if fs := rule.Check([]Entry{bad}); len(fs) != 0 {
		t.Errorf("entries with ParseErr must be skipped, got %+v", fs)
	}
}

// parseErrStub is a trivial error used to mark an Entry as unparseable.
type parseErrStub struct{}

func (*parseErrStub) Error() string { return "stub parse error" }

func TestLintWithLocation_SurfacesDSTAcrossCrontab(t *testing.T) {
	loc := loadTZ(t, "America/New_York")
	crontab := strings.NewReader(
		"30 2 * * * /backup\n" + // gap → warning
			"0 4 * * * /safe\n", // clear
	)
	report, err := LintWithLocation(crontab, loc, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("LintWithLocation: %v", err)
	}
	var dst *Finding
	for i := range report.Findings {
		if report.Findings[i].Rule == "dst-danger" {
			dst = &report.Findings[i]
		}
	}
	if dst == nil {
		t.Fatalf("expected a dst-danger finding in the report, got %+v", report.Findings)
	}
	if dst.Lines == nil || dst.Lines[0] != 1 {
		t.Errorf("dst finding should point at line 1, got %v", dst.Lines)
	}
}

func TestLint_WithoutLocationHasNoDSTFindings(t *testing.T) {
	// The plain Lint entrypoint stays UTC-only: a DST-hazardous schedule must
	// not produce a dst-danger finding when no timezone is in play.
	crontab := strings.NewReader("30 2 * * * /backup\n")
	report, err := Lint(crontab, nil)
	if err != nil {
		t.Fatalf("Lint: %v", err)
	}
	for _, f := range report.Findings {
		if f.Rule == "dst-danger" {
			t.Errorf("plain Lint must not emit dst-danger findings, got %+v", f)
		}
	}
}

func TestCheckScheduleTZ_AddsDSTRuleForTUI(t *testing.T) {
	loc := loadTZ(t, "America/New_York")
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sched := mustParse(t, "30 2 * * *")

	// The TUI-facing TZ check should surface the gap...
	withTZ := CheckScheduleTZ(sched, loc, now)
	var sawDST bool
	for _, f := range withTZ {
		if f.Rule == "dst-danger" {
			sawDST = true
		}
	}
	if !sawDST {
		t.Errorf("CheckScheduleTZ should include a dst-danger finding, got %+v", withTZ)
	}

	// ...while the plain single-schedule check (no zone) stays quiet about DST.
	for _, f := range CheckSchedule(sched) {
		if f.Rule == "dst-danger" {
			t.Errorf("CheckSchedule must not include dst-danger, got %+v", f)
		}
	}
}
