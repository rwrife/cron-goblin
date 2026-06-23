// Package nextrun computes the times a parse.Schedule actually fires.
//
// It is the M3 engine: given a normalized Schedule and a starting instant, it
// produces the upcoming fire times in a chosen timezone. Like parse and
// explain, this package is pure and deterministic — same Schedule, same start,
// same timezone in; same timestamps out. The goblin's personality lives in
// internal/goblin, not here.
//
// # Matching rules
//
// A minute matches when its minute, hour, and month fields all match the
// schedule AND the day matches. The day rule follows standard cron's famous
// day-of-month / day-of-week OR-behavior:
//
//   - If both DOM and DOW are restricted (neither was a bare "*"), the day
//     matches when EITHER the day-of-month OR the day-of-week matches.
//   - If only one of them is restricted, only that one is consulted.
//   - If both are "*", every day matches.
//
// This mirrors how Vixie/cron and most implementations behave, and matches the
// phrasing internal/explain already produces ("on the 13th or on Friday,
// whichever matches first").
//
// # Termination
//
// Some expressions never fire — e.g. `0 0 30 2 *` (February 30th). Rather than
// loop forever, the search is bounded by a horizon (see DefaultHorizonYears).
// When no fire time exists within the horizon, callers get ErrNeverFires.
package nextrun

import (
	"errors"
	"time"

	"github.com/rwrife/cron-goblin/internal/parse"
)

// DefaultHorizonYears bounds how far ahead the engine will search before it
// concludes a schedule never fires. Eight years comfortably clears the worst
// legitimate sparsity (a specific weekday-of-a-specific-date in a leap-aware
// month) while still terminating quickly on truly dead expressions.
const DefaultHorizonYears = 8

// ErrNeverFires is returned when a schedule has no matching time within the
// search horizon. The canonical example is February 30th (`* * 30 2 *`).
var ErrNeverFires = errors.New("schedule never fires")

// matcher precomputes per-field lookup tables from a Schedule so the hot loop
// does O(1) membership checks instead of scanning slices each minute.
type matcher struct {
	minute [60]bool
	hour   [24]bool
	dom    [32]bool // index 1..31 used
	month  [13]bool // index 1..12 used
	dow    [7]bool  // index 0..6, Sunday=0

	// domRestricted/dowRestricted record whether each day field was something
	// other than a bare "*", which drives the OR vs AND day rule.
	domRestricted bool
	dowRestricted bool
}

// newMatcher builds the lookup tables for a schedule.
func newMatcher(s parse.Schedule) matcher {
	var m matcher
	for _, v := range s.Minute.Values {
		if v >= 0 && v < len(m.minute) {
			m.minute[v] = true
		}
	}
	for _, v := range s.Hour.Values {
		if v >= 0 && v < len(m.hour) {
			m.hour[v] = true
		}
	}
	for _, v := range s.DOM.Values {
		if v >= 0 && v < len(m.dom) {
			m.dom[v] = true
		}
	}
	for _, v := range s.Month.Values {
		if v >= 0 && v < len(m.month) {
			m.month[v] = true
		}
	}
	for _, v := range s.DOW.Values {
		if v >= 0 && v < len(m.dow) {
			m.dow[v] = true
		}
	}

	// A field is "restricted" for the OR-rule when it was not written as a bare
	// "*". parse records that via FieldSpec.Star.
	m.domRestricted = !s.DOM.Star
	m.dowRestricted = !s.DOW.Star
	return m
}

// dayMatches applies cron's DOM/DOW OR-rule for a given calendar day.
func (m matcher) dayMatches(t time.Time) bool {
	domOK := m.dom[t.Day()]
	dowOK := m.dow[int(t.Weekday())]

	switch {
	case m.domRestricted && m.dowRestricted:
		// Classic cron OR: either day-of-month or day-of-week may satisfy it.
		return domOK || dowOK
	case m.domRestricted:
		return domOK
	case m.dowRestricted:
		return dowOK
	default:
		// Both are "*": every day qualifies.
		return true
	}
}

// matches reports whether the instant t (already in the target location) is a
// firing minute. It assumes t is minute-aligned (zero seconds/nanos).
func (m matcher) matches(t time.Time) bool {
	if !m.month[int(t.Month())] {
		return false
	}
	if !m.dayMatches(t) {
		return false
	}
	if !m.hour[t.Hour()] {
		return false
	}
	return m.minute[t.Minute()]
}

// Next returns the first fire time strictly after `from`, evaluated in the
// given location. If loc is nil, time.UTC is used. It returns ErrNeverFires if
// no match exists within DefaultHorizonYears.
//
// The returned time is minute-aligned (seconds and nanoseconds zeroed) and
// carries the requested location.
//
// Iteration is over the target location's *wall clock* (year/month/day/hour/
// minute), not over absolute time. This is what makes daylight-saving behavior
// correct and well-defined:
//
//   - Spring-forward: a wall time that does not exist (e.g. 02:30 on a US
//     spring-forward day) is detected via time.Date normalization and skipped,
//     so the schedule simply does not fire in the missing hour.
//   - Fall-back: a repeated wall time (e.g. 01:30 twice) is constructed exactly
//     once, and time.Date resolves it to the first occurrence, so the schedule
//     fires once rather than twice.
func Next(s parse.Schedule, from time.Time, loc *time.Location) (time.Time, error) {
	if loc == nil {
		loc = time.UTC
	}
	m := newMatcher(s)

	// Begin at the next whole wall-clock minute after `from`.
	start := from.In(loc).Truncate(time.Minute).Add(time.Minute)
	deadlineYear := start.Year() + DefaultHorizonYears

	c := newCursor(start)
	for c.year <= deadlineYear {
		// Fast-forward across excluded months and days so dead/sparse
		// expressions terminate quickly instead of scanning every minute.
		if !m.month[c.month] {
			c.bumpMonth()
			continue
		}
		// Guard against impossible day-of-month for this month (e.g. day 30 in
		// February): if the day overflows the month, roll to the next month.
		if c.day > daysIn(c.year, c.month) {
			c.bumpMonth()
			continue
		}
		cand := time.Date(c.year, time.Month(c.month), c.day, c.hour, c.minute, 0, 0, loc)
		// Detect a non-existent wall time (spring-forward gap): time.Date
		// normalizes it forward, so the materialized fields won't match what we
		// asked for. Such minutes don't exist; skip without firing.
		exists := cand.Hour() == c.hour && cand.Minute() == c.minute &&
			cand.Day() == c.day && int(cand.Month()) == c.month

		if exists && m.dayMatchesYMD(cand) && m.hour[c.hour] && m.minute[c.minute] {
			return cand, nil
		}

		// Advance the wall clock, fast-forwarding whole days/hours we can rule
		// out regardless of the lower fields.
		switch {
		case !m.dayMatchesYMD(time.Date(c.year, time.Month(c.month), c.day, 12, 0, 0, 0, loc)):
			c.bumpDay()
		case !m.hour[c.hour]:
			c.bumpHour()
		default:
			c.bumpMinute()
		}
	}
	return time.Time{}, ErrNeverFires
}

// dayMatchesYMD applies the DOM/DOW OR-rule using the calendar day of t. It is
// identical in spirit to dayMatches but named to make the wall-clock iteration
// read clearly.
func (m matcher) dayMatchesYMD(t time.Time) bool { return m.dayMatches(t) }

// NextN returns up to n fire times strictly after `from`, in chronological
// order, evaluated in loc (nil → UTC). It stops early and returns whatever it
// found (possibly empty) if the schedule stops matching within the horizon, so
// dead expressions yield an empty slice rather than an error. For a single
// definitive answer (including the never-fires signal), use Next.
//
// n <= 0 returns an empty, non-nil slice.
func NextN(s parse.Schedule, from time.Time, n int, loc *time.Location) []time.Time {
	if n <= 0 {
		return []time.Time{}
	}
	out := make([]time.Time, 0, n)
	cur := from
	for i := 0; i < n; i++ {
		t, err := Next(s, cur, loc)
		if err != nil {
			break
		}
		out = append(out, t)
		cur = t
	}
	return out
}

// cursor is a mutable wall-clock position (year/month/day/hour/minute) used to
// scan forward through the target location's calendar. Incrementing a field
// resets all lower fields so the scan stays in canonical order. Month is 1-12
// and day overflow is handled by the caller via daysIn.
type cursor struct {
	year   int
	month  int // 1-12
	day    int // 1-31
	hour   int // 0-23
	minute int // 0-59
}

// newCursor seeds a cursor from a time's wall-clock fields.
func newCursor(t time.Time) cursor {
	return cursor{
		year:   t.Year(),
		month:  int(t.Month()),
		day:    t.Day(),
		hour:   t.Hour(),
		minute: t.Minute(),
	}
}

// bumpMinute advances one minute, rolling into hours/days/months/years.
func (c *cursor) bumpMinute() {
	c.minute++
	if c.minute > 59 {
		c.minute = 0
		c.bumpHour()
	}
}

// bumpHour advances to the top of the next hour, resetting minutes.
func (c *cursor) bumpHour() {
	c.minute = 0
	c.hour++
	if c.hour > 23 {
		c.hour = 0
		c.bumpDay()
	}
}

// bumpDay advances to the start of the next day, resetting hour/minute. Month
// rollover is by calendar length so we never produce e.g. Feb 30.
func (c *cursor) bumpDay() {
	c.minute, c.hour = 0, 0
	c.day++
	if c.day > daysIn(c.year, c.month) {
		c.day = 1
		c.month++
		if c.month > 12 {
			c.month = 1
			c.year++
		}
	}
}

// bumpMonth advances to the first day of the next month at 00:00.
func (c *cursor) bumpMonth() {
	c.minute, c.hour, c.day = 0, 0, 1
	c.month++
	if c.month > 12 {
		c.month = 1
		c.year++
	}
}

// daysIn returns the number of days in a given month, leap-year aware.
func daysIn(year, month int) int {
	// Day 0 of the next month is the last day of this month.
	return time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day()
}
