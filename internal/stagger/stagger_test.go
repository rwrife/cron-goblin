package stagger

import (
	"strings"
	"testing"
)

// minutesOf extracts the minute (first field) of each non-blank, non-comment,
// non-env crontab line, for asserting on rewritten output.
func minutesOf(t *testing.T, crontab string) []string {
	t.Helper()
	var mins []string
	for _, line := range strings.Split(crontab, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.Contains(strings.SplitN(trimmed, " ", 2)[0], "=") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) < 5 {
			continue
		}
		mins = append(mins, fields[0])
	}
	return mins
}

func TestAnalyzeDetectsAndSpreadsHerd(t *testing.T) {
	src := "0 9 * * * /a\n0 9 * * * /b\n0 9 * * * /c\n"
	plan, err := Analyze(src, 30)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(plan.Herds) != 1 {
		t.Fatalf("want 1 herd, got %d: %+v", len(plan.Herds), plan.Herds)
	}
	h := plan.Herds[0]
	if h.Signature != "0 9 * * *" {
		t.Errorf("signature = %q, want %q", h.Signature, "0 9 * * *")
	}
	if len(h.Moves) != 3 {
		t.Fatalf("want 3 moves, got %d", len(h.Moves))
	}
	// Anchor (lowest line) stays at 0; others spread to 15 and 30 within 30 min.
	wantTo := []int{0, 15, 30}
	for i, m := range h.Moves {
		if m.ToMinute != wantTo[i] {
			t.Errorf("move %d ToMinute = %d, want %d", i, m.ToMinute, wantTo[i])
		}
	}
	if plan.MovedLines() != 2 {
		t.Errorf("MovedLines = %d, want 2", plan.MovedLines())
	}
}

func TestRewritePreservesEverythingElse(t *testing.T) {
	src := "# header comment\n" +
		"SHELL=/bin/bash\n" +
		"0 9 * * * /a\n" +
		"0 9 * * * /b\n" +
		"*/15 * * * * /poll\n" +
		"30 2 * * * /lonely\n"
	plan, err := Analyze(src, 59)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	out := plan.Rewrite(src)

	// Comment, env line, stepped job, and lonely job survive byte-for-byte.
	for _, want := range []string{"# header comment", "SHELL=/bin/bash", "*/15 * * * * /poll", "30 2 * * * /lonely"} {
		if !strings.Contains(out, want) {
			t.Errorf("rewrite dropped %q:\n%s", want, out)
		}
	}
	// The herd's two jobs now differ in minute.
	if !strings.Contains(out, "0 9 * * * /a") {
		t.Errorf("anchor job changed unexpectedly:\n%s", out)
	}
	if strings.Contains(out, "0 9 * * * /b") {
		t.Errorf("second herd job was not staggered:\n%s", out)
	}
}

func TestAnalyzeIgnoresNonFixedMinutes(t *testing.T) {
	// Stepped, listed, ranged, and star minutes are never staggered, even when
	// the rest of the schedule matches.
	cases := []string{
		"*/15 9 * * * /a\n*/15 9 * * * /b\n",
		"0,30 9 * * * /a\n0,30 9 * * * /b\n",
		"0-5 9 * * * /a\n0-5 9 * * * /b\n",
		"* 9 * * * /a\n* 9 * * * /b\n",
	}
	for _, src := range cases {
		plan, err := Analyze(src, 30)
		if err != nil {
			t.Fatalf("Analyze(%q): %v", src, err)
		}
		if !plan.Empty() {
			t.Errorf("expected no herds for %q, got: %+v", src, plan.Herds)
		}
	}
}

func TestAnalyzeDifferentHoursAreNotAHerd(t *testing.T) {
	// Same minute but different hours do not collide, so they are not a herd.
	src := "0 9 * * * /a\n0 10 * * * /b\n0 11 * * * /c\n"
	plan, err := Analyze(src, 30)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if !plan.Empty() {
		t.Errorf("expected no herds, got: %+v", plan.Herds)
	}
}

func TestAnalyzeLoneJobIsNotAHerd(t *testing.T) {
	src := "0 9 * * * /a\n"
	plan, err := Analyze(src, 30)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if !plan.Empty() {
		t.Errorf("single job should not be a herd, got: %+v", plan.Herds)
	}
}

func TestAnalyzeMultipleHerds(t *testing.T) {
	src := "0 9 * * * /a\n0 9 * * * /b\n" + // herd 1
		"30 0 1 * * /m\n30 0 1 * * /n\n" // herd 2 (monthly)
	plan, err := Analyze(src, 59)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(plan.Herds) != 2 {
		t.Fatalf("want 2 herds, got %d: %+v", len(plan.Herds), plan.Herds)
	}
	if plan.MovedLines() != 2 {
		t.Errorf("MovedLines = %d, want 2", plan.MovedLines())
	}
}

func TestSpreadMinutesEvenAndBounded(t *testing.T) {
	got := spreadMinutes(0, 4, 60)
	want := []int{0, 20, 40, 59}
	// 60/3 = 20 step → 0,20,40,60; 60 clamps to 59.
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("spread[%d] = %d, want %d (full: %v)", i, got[i], want[i], got)
		}
	}
}

func TestSpreadMinutesAnchorOffsetClamps(t *testing.T) {
	// Anchored late in the hour: spread must clamp at 59 and stay non-decreasing.
	got := spreadMinutes(50, 5, 59)
	if got[0] != 50 {
		t.Errorf("anchor = %d, want 50", got[0])
	}
	for i := 1; i < len(got); i++ {
		if got[i] < got[i-1] {
			t.Errorf("spread not non-decreasing at %d: %v", i, got)
		}
		if got[i] > 59 || got[i] < 0 {
			t.Errorf("spread[%d] = %d out of range: %v", i, got[i], got)
		}
	}
}

func TestSpreadMinutesLargeHerdPacksOneApart(t *testing.T) {
	// More jobs than the window can evenly hold: step floors to 1.
	got := spreadMinutes(0, 10, 5)
	if got[0] != 0 {
		t.Errorf("anchor = %d, want 0", got[0])
	}
	for i := 1; i < len(got); i++ {
		if got[i] <= got[i-1] && got[i-1] < 59 {
			t.Errorf("expected strictly increasing while room remains, got %v", got)
		}
	}
}

func TestRewriteRoundTripsNoHerd(t *testing.T) {
	src := "0 9 * * * /a\n0 10 * * * /b\n"
	plan, err := Analyze(src, 30)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if out := plan.Rewrite(src); out != src {
		t.Errorf("no-op rewrite changed text:\nwant %q\ngot  %q", src, out)
	}
}

func TestReplaceMinuteTokenPreservesSpacing(t *testing.T) {
	// Tabs and extra spaces around the command must survive a minute swap.
	in := "0\t9 * * *   /weird/command --flag"
	out := replaceMinuteToken(in, 17)
	if !strings.HasPrefix(out, "17\t9 * * *") {
		t.Errorf("spacing not preserved: %q", out)
	}
	if !strings.HasSuffix(out, "/weird/command --flag") {
		t.Errorf("command mangled: %q", out)
	}
}

func TestAnalyzeSkipsMalformedLines(t *testing.T) {
	// A bad line must not crash analysis; valid herd is still found.
	src := "this is not cron\n0 9 * * * /a\n0 9 * * * /b\n"
	plan, err := Analyze(src, 30)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(plan.Herds) != 1 {
		t.Fatalf("want 1 herd despite malformed line, got %d", len(plan.Herds))
	}
}

func TestAnalyzeDefaultMaxSpread(t *testing.T) {
	src := "0 9 * * * /a\n0 9 * * * /b\n"
	plan, err := Analyze(src, 0) // 0 → DefaultMaxSpread
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if plan.MaxSpread != DefaultMaxSpread {
		t.Errorf("MaxSpread = %d, want default %d", plan.MaxSpread, DefaultMaxSpread)
	}
}
