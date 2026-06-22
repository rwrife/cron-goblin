package nextrun

import (
	"testing"
	"time"

	"github.com/rwrife/cron-goblin/internal/parse"
)

// mustParse parses or fails the test.
func mustParse(t *testing.T, expr string) parse.Schedule {
	t.Helper()
	s, err := parse.Parse(expr)
	if err != nil {
		t.Fatalf("parse(%q): %v", expr, err)
	}
	return s
}

// at is a terse constructor for a UTC instant.
func at(y int, mo time.Month, d, h, mi int) time.Time {
	return time.Date(y, mo, d, h, mi, 0, 0, time.UTC)
}

// TestNextEveryFifteenMinutes is a core acceptance check from issue #3:
// `*/15 * * * *` must step cleanly on :00/:15/:30/:45.
func TestNextEveryFifteenMinutes(t *testing.T) {
	s := mustParse(t, "*/15 * * * *")
	// Start at 10:07 → expect 10:15, 10:30, 10:45, 11:00, 11:15.
	from := at(2026, time.June, 22, 10, 7)
	want := []time.Time{
		at(2026, time.June, 22, 10, 15),
		at(2026, time.June, 22, 10, 30),
		at(2026, time.June, 22, 10, 45),
		at(2026, time.June, 22, 11, 0),
		at(2026, time.June, 22, 11, 15),
	}
	got := NextN(s, from, len(want), time.UTC)
	if len(got) != len(want) {
		t.Fatalf("NextN len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Errorf("run[%d] = %s, want %s", i, got[i].Format(time.RFC3339), want[i].Format(time.RFC3339))
		}
	}
}

// TestNextOnTheMinuteBoundaryIsStrictlyAfter ensures `from` exactly on a fire
// minute yields the NEXT one, never `from` itself.
func TestNextStrictlyAfter(t *testing.T) {
	s := mustParse(t, "*/15 * * * *")
	from := at(2026, time.June, 22, 10, 15) // itself a fire time
	got, err := Next(s, from, time.UTC)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	want := at(2026, time.June, 22, 10, 30)
	if !got.Equal(want) {
		t.Errorf("Next = %s, want %s (must be strictly after)", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

// TestDeadExpressionNeverFires is the headline acceptance check: February 30th
// can never happen, so Next reports ErrNeverFires and NextN returns empty.
func TestDeadExpressionNeverFires(t *testing.T) {
	s := mustParse(t, "0 0 30 2 *")
	from := at(2026, time.January, 1, 0, 0)

	if _, err := Next(s, from, time.UTC); err != ErrNeverFires {
		t.Errorf("Next err = %v, want ErrNeverFires", err)
	}
	if runs := NextN(s, from, 5, time.UTC); len(runs) != 0 {
		t.Errorf("NextN = %v, want empty for a dead expression", runs)
	}
}

// TestDOMDOWOrRule verifies cron's famous OR-behavior: with BOTH day-of-month
// and day-of-week restricted, a day matches if EITHER matches.
// `0 0 13 * 5` fires at midnight on the 13th OR on any Friday.
func TestDOMDOWOrRule(t *testing.T) {
	s := mustParse(t, "0 0 13 * 5")
	// Start just after midnight on 2026-06-01 (a Monday).
	from := at(2026, time.June, 1, 0, 1)
	got := NextN(s, from, 6, time.UTC)

	// June 2026: Fridays are 5,12,19,26; the 13th is a Saturday.
	// Expected union (sorted): Jun 5, 12, 13, 19, 26, then Jul 3.
	want := []time.Time{
		at(2026, time.June, 5, 0, 0),
		at(2026, time.June, 12, 0, 0),
		at(2026, time.June, 13, 0, 0), // 13th via DOM even though it's Saturday
		at(2026, time.June, 19, 0, 0),
		at(2026, time.June, 26, 0, 0),
		at(2026, time.July, 3, 0, 0),
	}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Errorf("run[%d] = %s, want %s", i, got[i].Format(time.RFC3339), want[i].Format(time.RFC3339))
		}
	}
}

// TestDOMOnlyRestricted: when DOW is "*", only day-of-month applies (no OR
// widening). `0 12 1 * *` fires only on the 1st of each month at noon.
func TestDOMOnlyRestricted(t *testing.T) {
	s := mustParse(t, "0 12 1 * *")
	from := at(2026, time.June, 10, 0, 0)
	got := NextN(s, from, 3, time.UTC)
	want := []time.Time{
		at(2026, time.July, 1, 12, 0),
		at(2026, time.August, 1, 12, 0),
		at(2026, time.September, 1, 12, 0),
	}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Errorf("run[%d] = %s, want %s", i, got[i].Format(time.RFC3339), want[i].Format(time.RFC3339))
		}
	}
}

// TestDOWOnlyRestricted: when DOM is "*", only day-of-week applies.
// `0 9 * * 1` fires every Monday at 09:00.
func TestDOWOnlyRestricted(t *testing.T) {
	s := mustParse(t, "0 9 * * 1")
	from := at(2026, time.June, 22, 10, 0) // Monday 22nd, after 09:00
	got := NextN(s, from, 3, time.UTC)
	want := []time.Time{
		at(2026, time.June, 29, 9, 0),
		at(2026, time.July, 6, 9, 0),
		at(2026, time.July, 13, 9, 0),
	}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Errorf("run[%d] = %s, want %s", i, got[i].Format(time.RFC3339), want[i].Format(time.RFC3339))
		}
	}
}

// TestNamedMonthAndWeekday checks named fields resolve correctly:
// `0 0 * DEC SUN` fires midnight every Sunday in December.
func TestNamedMonthAndWeekday(t *testing.T) {
	s := mustParse(t, "0 0 * DEC SUN")
	from := at(2026, time.November, 1, 0, 0)
	got := NextN(s, from, 2, time.UTC)
	// December 2026 Sundays: 6, 13, 20, 27.
	want := []time.Time{
		at(2026, time.December, 6, 0, 0),
		at(2026, time.December, 13, 0, 0),
	}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Errorf("run[%d] = %s, want %s", i, got[i].Format(time.RFC3339), want[i].Format(time.RFC3339))
		}
	}
}

// TestLeapDay confirms Feb 29 only fires in leap years (2028, not 2026/2027).
func TestLeapDay(t *testing.T) {
	s := mustParse(t, "0 0 29 2 *")
	from := at(2026, time.March, 1, 0, 0)
	got, err := Next(s, from, time.UTC)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	want := at(2028, time.February, 29, 0, 0)
	if !got.Equal(want) {
		t.Errorf("Next = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

// TestTimezoneAffectsWallClock verifies fire times are computed against the
// requested location's wall clock. `0 9 * * *` is 09:00 LOCAL, which is a
// different UTC instant in New York than in UTC.
func TestTimezoneAffectsWallClock(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	s := mustParse(t, "0 9 * * *")
	from := at(2026, time.June, 22, 0, 0) // midnight UTC
	got, err := Next(s, from, ny)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	// 09:00 in New York (EDT, UTC-4 in June) == 13:00 UTC.
	if got.Hour() != 9 {
		t.Errorf("wall-clock hour = %d, want 9 (local)", got.Hour())
	}
	if u := got.UTC(); u.Hour() != 13 {
		t.Errorf("UTC hour = %d, want 13 (09:00 EDT)", u.Hour())
	}
}

// TestDSTSpringForwardSkipsMissingHour: in US spring-forward 2026 (Mar 8),
// the wall clock jumps 02:00 EST -> 03:00 EDT, so 02:30 never exists that day.
// A `30 2 * * *` schedule must skip Mar 8 entirely and fire Mar 7 then Mar 9.
func TestDSTSpringForwardSkipsMissingHour(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	s := mustParse(t, "30 2 * * *")
	// Start just after midnight on Mar 7 (local) so Mar 7's 02:30 is upcoming.
	from := time.Date(2026, time.March, 7, 0, 0, 0, 0, ny)
	got := NextN(s, from, 2, ny)
	if len(got) != 2 {
		t.Fatalf("got %d runs, want 2: %v", len(got), got)
	}

	// Every emitted run must be a real 02:30 local instant...
	for i, r := range got {
		if r.Hour() != 2 || r.Minute() != 30 {
			t.Errorf("run[%d] = %s, want a real 02:30 local time", i, r.Format(time.RFC3339))
		}
	}
	// ...and Mar 8 must be skipped: the two runs are Mar 7 and Mar 9.
	if d := got[0].Day(); d != 7 {
		t.Errorf("run[0] day = %d, want 7", d)
	}
	if d := got[1].Day(); d != 9 {
		t.Errorf("run[1] day = %d, want 9 (Mar 8 skipped by DST gap)", d)
	}
	if !got[1].After(got[0]) {
		t.Errorf("runs not increasing: %v", got)
	}
}

// TestDSTFallBackNoDoubleFire: in US fall-back 2026 (Nov 1), 01:30 occurs
// twice. Cron fires it once (the first occurrence); the engine must not emit
// two runs for the same wall time on that day.
func TestDSTFallBackNoDoubleFire(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	s := mustParse(t, "30 1 * * *")
	from := time.Date(2026, time.October, 31, 12, 0, 0, 0, ny)
	got := NextN(s, from, 3, ny)
	if len(got) != 3 {
		t.Fatalf("got %d runs, want 3: %v", len(got), got)
	}
	// Expect Nov 1, Nov 2, Nov 3 — exactly one 01:30 on the fall-back day.
	wantDays := []int{1, 2, 3}
	for i, wd := range wantDays {
		if got[i].Day() != wd || got[i].Month() != time.November {
			t.Errorf("run[%d] = %s, want Nov %d 01:30", i, got[i].Format(time.RFC3339), wd)
		}
		if got[i].Hour() != 1 || got[i].Minute() != 30 {
			t.Errorf("run[%d] = %s, want 01:30 local", i, got[i].Format(time.RFC3339))
		}
	}
}

// TestNilLocationDefaultsUTC ensures a nil location is treated as UTC.
func TestNilLocationDefaultsUTC(t *testing.T) {
	s := mustParse(t, "0 0 * * *")
	from := at(2026, time.June, 22, 1, 0)
	got, err := Next(s, from, nil)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	want := at(2026, time.June, 23, 0, 0)
	if !got.Equal(want) || got.Location() != time.UTC {
		t.Errorf("Next = %s (loc %v), want %s UTC", got.Format(time.RFC3339), got.Location(), want.Format(time.RFC3339))
	}
}

// TestNextNZeroOrNegative returns an empty, non-nil slice.
func TestNextNNonPositive(t *testing.T) {
	s := mustParse(t, "* * * * *")
	from := at(2026, time.June, 22, 0, 0)
	if got := NextN(s, from, 0, time.UTC); got == nil || len(got) != 0 {
		t.Errorf("NextN(...,0) = %v, want empty non-nil", got)
	}
	if got := NextN(s, from, -3, time.UTC); len(got) != 0 {
		t.Errorf("NextN(...,-3) = %v, want empty", got)
	}
}

// TestEveryMinute is the densest case: consecutive minutes with no gaps.
func TestEveryMinute(t *testing.T) {
	s := mustParse(t, "* * * * *")
	from := at(2026, time.June, 22, 23, 58)
	got := NextN(s, from, 4, time.UTC)
	want := []time.Time{
		at(2026, time.June, 22, 23, 59),
		at(2026, time.June, 23, 0, 0),
		at(2026, time.June, 23, 0, 1),
		at(2026, time.June, 23, 0, 2),
	}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Errorf("run[%d] = %s, want %s", i, got[i].Format(time.RFC3339), want[i].Format(time.RFC3339))
		}
	}
}
