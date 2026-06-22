// Package explain renders a normalized parse.Schedule into plain English.
//
// The goal is output that "reads like a human wrote it" — not a mechanical
// field-by-field dump. We special-case the common shapes people actually
// write (every minute, every N minutes, a fixed daily time, weekday ranges)
// and fall back to descriptive phrasing for the long tail.
//
// This package is pure and deterministic: same Schedule in, same sentence
// out. The goblin's personality lives in internal/goblin, not here.
package explain

import (
	"fmt"
	"strings"

	"github.com/rwrife/cron-goblin/internal/parse"
)

// monthLong maps a month number (1-12) to its full English name.
var monthLong = [...]string{
	"", "January", "February", "March", "April", "May", "June",
	"July", "August", "September", "October", "November", "December",
}

// dowLong maps a day-of-week number (0-6, Sunday=0) to its full English name.
var dowLong = [...]string{
	"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday",
}

// Explain returns a single plain-English sentence describing when the schedule
// fires. The sentence is capitalized and has no trailing period, so callers
// can compose it freely.
func Explain(s parse.Schedule) string {
	timePart := explainTime(s)
	dayPart := explainDays(s)

	var b strings.Builder
	b.WriteString(timePart)
	if dayPart != "" {
		b.WriteString(" ")
		b.WriteString(dayPart)
	}

	out := b.String()
	return capitalize(strings.TrimSpace(out))
}

// explainTime describes the minute+hour portion ("the time of day").
func explainTime(s parse.Schedule) string {
	min := s.Minute
	hr := s.Hour

	minStep, minStepOK := steppedStar(min, 60)
	hrStep, hrStepOK := steppedStar(hr, 24)

	switch {
	// "* * ..." → every minute.
	case min.Star && hr.Star:
		return "every minute"

	// "*/n * ..." → every n minutes.
	case minStepOK && hr.Star:
		if minStep == 1 {
			return "every minute"
		}
		return fmt.Sprintf("every %d minutes", minStep)

	// "*/n h ..." → every n minutes, but only during specific hours.
	case minStepOK && !hr.Star:
		if minStep == 1 {
			return "every minute during " + hourPhrase(hr)
		}
		return fmt.Sprintf("every %d minutes during %s", minStep, hourPhrase(hr))

	// "m */n ..." → at minute m past every nth hour.
	case len(min.Values) == 1 && hrStepOK:
		if hrStep == 1 {
			return fmt.Sprintf("at %d minutes past every hour", min.Values[0])
		}
		return fmt.Sprintf("at minute %d past every %d hours", min.Values[0], hrStep)

	// "m * ..." → at minute m past every hour.
	case len(min.Values) == 1 && hr.Star:
		if min.Values[0] == 0 {
			return "at the top of every hour"
		}
		return fmt.Sprintf("at %d minutes past every hour", min.Values[0])

	// "* h ..." → every minute during hour(s) h.
	case min.Star && len(hr.Values) >= 1:
		return "every minute during " + hourPhrase(hr)

	// Single concrete time: "m h ..." → at H:MM.
	case len(min.Values) == 1 && len(hr.Values) == 1:
		return "at " + clock(hr.Values[0], min.Values[0])

	// Minute fixed, several specific hours: "at MM past N, M and K o'clock"
	case len(min.Values) == 1 && len(hr.Values) > 1:
		times := make([]string, len(hr.Values))
		for i, h := range hr.Values {
			times[i] = clock(h, min.Values[0])
		}
		return "at " + joinList(times)

	// Fallback: describe each part on its own.
	default:
		return fmt.Sprintf("at %s past %s", minutePhrase(min), hourPhrase(hr))
	}
}

// explainDays describes the day-of-month / month / day-of-week portion.
//
// Cron's quirk: if BOTH day-of-month and day-of-week are restricted (neither
// is "*"), a run happens when EITHER matches (a union, not an intersection).
// We surface that explicitly so nobody is surprised.
func explainDays(s parse.Schedule) string {
	dom := s.DOM
	month := s.Month
	dow := s.DOW

	var clauses []string

	domRestricted := !dom.Star
	dowRestricted := !dow.Star

	switch {
	case domRestricted && dowRestricted:
		// The OR rule. Make it loud.
		clauses = append(clauses, fmt.Sprintf("on %s or on %s (whichever matches first)",
			domPhrase(dom), dowPhrase(dow)))
	case domRestricted:
		clauses = append(clauses, "on "+domPhrase(dom))
	case dowRestricted:
		clauses = append(clauses, "on "+dowPhrase(dow))
	default:
		clauses = append(clauses, "every day")
	}

	if !month.Star {
		clauses = append(clauses, "in "+monthPhrase(month))
	}

	return strings.Join(clauses, " ")
}

// --- field phrasing helpers -------------------------------------------------

// minutePhrase describes a minute field that isn't a simple single value.
func minutePhrase(f parse.FieldSpec) string {
	if f.Star {
		return "every minute"
	}
	if step, ok := steppedStar(f, 60); ok {
		return fmt.Sprintf("every %d minutes", step)
	}
	return "minute " + numberList(f.Values)
}

// hourPhrase describes an hour field as "N o'clock" style phrasing.
func hourPhrase(f parse.FieldSpec) string {
	if f.Star {
		return "every hour"
	}
	if step, ok := steppedStar(f, 24); ok {
		return fmt.Sprintf("every %d hours", step)
	}
	if lo, hi, ok := contiguous(f.Values); ok && hi > lo {
		return fmt.Sprintf("the hours %s–%s", clockHour(lo), clockHour(hi))
	}
	parts := make([]string, len(f.Values))
	for i, h := range f.Values {
		parts[i] = clockHour(h)
	}
	return joinList(parts)
}

// domPhrase describes the day-of-month field ("the 1st", "the 1st and 15th").
func domPhrase(f parse.FieldSpec) string {
	if step, ok := steppedStarFrom(f, 1, 31); ok {
		return fmt.Sprintf("every %d days of the month", step)
	}
	if lo, hi, ok := contiguous(f.Values); ok && hi > lo {
		return fmt.Sprintf("the %s through the %s", ordinal(lo), ordinal(hi))
	}
	parts := make([]string, len(f.Values))
	for i, d := range f.Values {
		parts[i] = ordinal(d)
	}
	return "the " + joinList(parts)
}

// monthPhrase describes the month field using full month names.
func monthPhrase(f parse.FieldSpec) string {
	if lo, hi, ok := contiguous(f.Values); ok && hi > lo {
		return fmt.Sprintf("%s through %s", monthLong[lo], monthLong[hi])
	}
	parts := make([]string, len(f.Values))
	for i, m := range f.Values {
		if m >= 1 && m <= 12 {
			parts[i] = monthLong[m]
		} else {
			parts[i] = fmt.Sprintf("month %d", m)
		}
	}
	return joinList(parts)
}

// dowPhrase describes the day-of-week field using full weekday names, with a
// friendly shortcut for the Mon–Fri "weekdays" range.
func dowPhrase(f parse.FieldSpec) string {
	if isWeekdays(f.Values) {
		return "weekdays (Monday through Friday)"
	}
	if isWeekend(f.Values) {
		return "weekends (Saturday and Sunday)"
	}
	if lo, hi, ok := contiguous(f.Values); ok && hi > lo {
		return fmt.Sprintf("%s through %s", dowLong[lo], dowLong[hi])
	}
	parts := make([]string, len(f.Values))
	for i, d := range f.Values {
		if d >= 0 && d <= 6 {
			parts[i] = dowLong[d]
		} else {
			parts[i] = fmt.Sprintf("weekday %d", d)
		}
	}
	return joinList(parts)
}

// --- small utilities --------------------------------------------------------

// steppedStar reports whether f looks like "*/n" over a 0-based field of the
// given size (e.g. 60 for minutes), returning the step n. It detects an evenly
// spaced sequence starting at 0 that covers the whole range.
func steppedStar(f parse.FieldSpec, size int) (step int, ok bool) {
	return steppedStarFrom(f, 0, size-1)
}

// steppedStarFrom is steppedStar generalized to an arbitrary [min,max] range,
// used for 1-based fields like day-of-month. It requires at least three evenly
// spaced values so a sparse two-element list (e.g. "0,12") is described as
// specific values rather than a misleading "every N".
func steppedStarFrom(f parse.FieldSpec, min, max int) (step int, ok bool) {
	if f.Star || len(f.Values) < 3 {
		return 0, false
	}
	if f.Values[0] != min {
		return 0, false
	}
	step = f.Values[1] - f.Values[0]
	if step < 2 {
		return 0, false
	}
	// Every value must be evenly spaced...
	for i := 1; i < len(f.Values); i++ {
		if f.Values[i]-f.Values[i-1] != step {
			return 0, false
		}
	}
	// ...and the sequence must extend to (or just past) the field max, so we
	// don't mistake a short list like "0,15" for a true "*/15".
	if f.Values[len(f.Values)-1]+step <= max {
		return 0, false
	}
	return step, true
}

// contiguous reports whether vals is a gap-free ascending run, returning its
// endpoints. Single-element sets are contiguous with lo == hi.
func contiguous(vals []int) (lo, hi int, ok bool) {
	if len(vals) == 0 {
		return 0, 0, false
	}
	for i := 1; i < len(vals); i++ {
		if vals[i] != vals[i-1]+1 {
			return 0, 0, false
		}
	}
	return vals[0], vals[len(vals)-1], true
}

// isWeekdays reports whether vals is exactly {1,2,3,4,5} (Mon–Fri).
func isWeekdays(vals []int) bool { return equalSet(vals, []int{1, 2, 3, 4, 5}) }

// isWeekend reports whether vals is exactly {0,6} (Sun, Sat).
func isWeekend(vals []int) bool { return equalSet(vals, []int{0, 6}) }

func equalSet(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// clock formats an hour+minute as a 24-hour "HH:MM" wall-clock time.
func clock(h, m int) string { return fmt.Sprintf("%02d:%02d", h, m) }

// clockHour formats a bare hour as "HH:00".
func clockHour(h int) string { return fmt.Sprintf("%02d:00", h) }

// numberList joins ints with commas and a trailing "and".
func numberList(vals []int) string {
	parts := make([]string, len(vals))
	for i, v := range vals {
		parts[i] = fmt.Sprintf("%d", v)
	}
	return joinList(parts)
}

// joinList renders a human list: "a", "a and b", or "a, b and c".
func joinList(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " and " + parts[1]
	default:
		return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
	}
}

// ordinal renders 1 -> "1st", 2 -> "2nd", 21 -> "21st", etc.
func ordinal(n int) string {
	suffix := "th"
	if n%100 < 11 || n%100 > 13 {
		switch n % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return fmt.Sprintf("%d%s", n, suffix)
}

// capitalize upper-cases the first rune of s.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
