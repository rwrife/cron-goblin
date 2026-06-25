// Package english turns a plain-English schedule phrase into a standard
// 5-field cron expression. It is the inverse of internal/explain.
//
// This package is the M6 "English -> cron" core. It is deliberately a small,
// hand-rolled rule grammar — NOT an LLM and NOT a fuzzy matcher. It must be
// deterministic and work fully offline (see PLAN.md §5/§9): the same phrase in
// always yields the same cron out, or a clear error.
//
// The supported surface intentionally covers the common 80% of what people
// actually type:
//
//	"every minute"                      -> * * * * *
//	"every 15 minutes"                  -> */15 * * * *
//	"every 2 hours"                     -> 0 */2 * * *
//	"every hour"                        -> 0 * * * *
//	"hourly"                            -> 0 * * * *
//	"every day at 9am"                  -> 0 9 * * *
//	"daily at 6:30pm"                   -> 30 18 * * *
//	"every weekday at 6:30pm"           -> 30 18 * * 1-5
//	"weekends at noon"                  -> 0 12 * * 0,6
//	"every monday at 8am"              -> 0 8 * * 1
//	"every tuesday and thursday at 5pm" -> 0 17 * * 2,4
//	"at midnight"                       -> 0 0 * * *
//	"first of the month at 9am"         -> 0 9 1 * *
//	"on the 15th at noon"               -> 0 12 15 * *
//	"every january at midnight"         -> 0 0 1 1 *  (first of January)
//
// Anything outside this grammar returns an error (with the goblin's blessing
// to grumble elsewhere) rather than guessing — a wrong cron line is worse than
// an honest "I didn't understand that".
package english

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// cron holds the five fields while we build them up from clauses. Each field
// defaults to "*"; helpers below set them as phrases are recognized.
type cron struct {
	minute, hour, dom, month, dow string
}

// newCron returns an all-"*" expression (i.e. "every minute").
func newCron() cron {
	return cron{minute: "*", hour: "*", dom: "*", month: "*", dow: "*"}
}

// String renders the five fields in canonical cron order.
func (c cron) String() string {
	return strings.Join([]string{c.minute, c.hour, c.dom, c.month, c.dow}, " ")
}

// Error is an English-parse failure. It carries the original phrase so callers
// (and the goblin) can echo what confused us.
type Error struct {
	Phrase string
	Msg    string
}

func (e *Error) Error() string {
	return fmt.Sprintf("could not turn %q into cron: %s", e.Phrase, e.Msg)
}

// errf is a small constructor for *Error with a formatted message.
func errf(phrase, format string, a ...any) error {
	return &Error{Phrase: phrase, Msg: fmt.Sprintf(format, a...)}
}

// dowNum maps weekday words (and common abbreviations) to cron numbers
// (Sunday = 0), matching internal/parse.
var dowNum = map[string]int{
	"sunday": 0, "sun": 0,
	"monday": 1, "mon": 1,
	"tuesday": 2, "tue": 2, "tues": 2,
	"wednesday": 3, "wed": 3,
	"thursday": 4, "thu": 4, "thur": 4, "thurs": 4,
	"friday": 5, "fri": 5,
	"saturday": 6, "sat": 6,
}

// monthNum maps month words (and three-letter abbreviations) to cron numbers.
var monthNum = map[string]int{
	"january": 1, "jan": 1,
	"february": 2, "feb": 2,
	"march": 3, "mar": 3,
	"april": 4, "apr": 4,
	"may":  5,
	"june": 6, "jun": 6,
	"july": 7, "jul": 7,
	"august": 8, "aug": 8,
	"september": 9, "sep": 9, "sept": 9,
	"october": 10, "oct": 10,
	"november": 11, "nov": 11,
	"december": 12, "dec": 12,
}

// ordinalWord maps spelled-out and suffixed ordinals to a day-of-month number.
// Only values that make sense for "the Nth of the month" are listed.
var ordinalWord = map[string]int{
	"first": 1, "second": 2, "third": 3, "fourth": 4, "fifth": 5,
	"last": -1, // handled specially: cron has no real "last", we reject it clearly
}

// timeRE matches an explicit clock time: "9", "9am", "9:30", "6:30pm",
// "12:00 am". Hour is required; minutes and the am/pm marker are optional.
var timeRE = regexp.MustCompile(`^(\d{1,2})(?::(\d{2}))?\s*(am|pm)?$`)

// everyNUnitRE matches "every 15 minutes" / "every 2 hours" (unit may be
// singular or plural). The leading "every " is stripped before matching.
var everyNUnitRE = regexp.MustCompile(`^(\d+)\s+(minute|minutes|min|mins|hour|hours|hr|hrs)$`)

// nthOfMonthRE matches "the 15th", "15th", "the 1st" → a day-of-month number.
var nthOfMonthRE = regexp.MustCompile(`^(?:the\s+)?(\d{1,2})(?:st|nd|rd|th)?$`)

// Parse converts an English schedule phrase into a 5-field cron string.
//
// The phrase is l-cased and whitespace-normalized first. Parsing splits the
// phrase into an optional time clause ("at ...") and a recurrence clause
// (everything before "at"), each handled independently. Unknown input yields
// an *Error.
func Parse(phrase string) (string, error) {
	norm := normalize(phrase)
	if norm == "" {
		return "", errf(phrase, "empty phrase")
	}

	// Split on the first standalone "at" that introduces a time-of-day, e.g.
	// "every weekday at 6:30pm" → recur="every weekday", timeStr="6:30pm".
	recur, timeStr := splitAtTime(norm)

	c := newCron()

	// 1) Apply the time-of-day clause, if present. This sets minute+hour.
	timeApplied := false
	if timeStr != "" {
		min, hr, err := parseTimeOfDay(phrase, timeStr)
		if err != nil {
			return "", err
		}
		c.minute, c.hour = strconv.Itoa(min), strconv.Itoa(hr)
		timeApplied = true
	}

	// 2) Apply the recurrence clause. This may set the period (every N minutes /
	// hours), or day-of-week / day-of-month / month restrictions. It also tells
	// us whether a sub-hour period was set, which conflicts with a fixed time.
	subHour, err := applyRecurrence(phrase, &c, recur, timeApplied)
	if err != nil {
		return "", err
	}

	// 3) Sanity: "every 5 minutes at 9am" is contradictory. Reject rather than
	// silently dropping one half.
	if subHour && timeApplied {
		return "", errf(phrase, "a specific time can't be combined with a sub-hourly repeat")
	}

	return c.String(), nil
}

// normalize lower-cases, collapses whitespace, and strips a few filler words
// and trailing punctuation so the grammar can stay small.
func normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimRight(s, ".!?")
	// Collapse internal whitespace runs to single spaces.
	s = strings.Join(strings.Fields(s), " ")
	// Drop leading politeness/filler that doesn't change meaning.
	for _, pre := range []string{"run ", "fire ", "trigger ", "execute "} {
		if strings.HasPrefix(s, pre) {
			s = strings.TrimSpace(s[len(pre):])
		}
	}
	return s
}

// splitAtTime separates a recurrence clause from a trailing time clause. It
// looks for " at " and treats what follows as the time only when it parses as
// a clock value or a named time-of-day (noon/midnight); otherwise the whole
// phrase is the recurrence (e.g. "first of the month" has no "at").
func splitAtTime(s string) (recur, timeStr string) {
	// Named whole-phrase times with no explicit "at".
	switch s {
	case "midnight", "at midnight":
		return "", "midnight"
	case "noon", "at noon", "midday", "at midday":
		return "", "noon"
	}

	idx := strings.LastIndex(s, " at ")
	if idx < 0 {
		// Phrase may itself start with "at ..." (e.g. "at 9am").
		if strings.HasPrefix(s, "at ") {
			return "", strings.TrimSpace(s[len("at "):])
		}
		return s, ""
	}
	recur = strings.TrimSpace(s[:idx])
	timeStr = strings.TrimSpace(s[idx+len(" at "):])
	return recur, timeStr
}

// parseTimeOfDay parses a time clause into minute and hour (24h). It accepts
// "noon", "midnight", and clock forms like "9", "9am", "6:30pm", "14:00".
func parseTimeOfDay(phrase, timeStr string) (minute, hour int, err error) {
	switch timeStr {
	case "noon", "midday":
		return 0, 12, nil
	case "midnight":
		return 0, 0, nil
	}

	m := timeRE.FindStringSubmatch(timeStr)
	if m == nil {
		return 0, 0, errf(phrase, "I don't understand the time %q", timeStr)
	}

	hour, _ = strconv.Atoi(m[1])
	if m[2] != "" {
		minute, _ = strconv.Atoi(m[2])
	}
	meridiem := m[3]

	if minute < 0 || minute > 59 {
		return 0, 0, errf(phrase, "minute %d is out of range (0-59)", minute)
	}

	switch meridiem {
	case "am":
		if hour < 1 || hour > 12 {
			return 0, 0, errf(phrase, "%d%s is not a valid 12-hour time", hour, meridiem)
		}
		if hour == 12 { // 12am == 00:00
			hour = 0
		}
	case "pm":
		if hour < 1 || hour > 12 {
			return 0, 0, errf(phrase, "%d%s is not a valid 12-hour time", hour, meridiem)
		}
		if hour != 12 { // 12pm == 12:00, otherwise add 12
			hour += 12
		}
	default:
		// No am/pm → interpret as 24-hour.
		if hour < 0 || hour > 23 {
			return 0, 0, errf(phrase, "hour %d is out of range (0-23); add am/pm for 12-hour times", hour)
		}
	}
	return minute, hour, nil
}

// applyRecurrence interprets the recurrence clause and mutates c accordingly.
// It returns subHour=true when the clause established a sub-hourly period
// (every N minutes), which the caller uses to reject a conflicting fixed time.
//
// timeApplied tells us whether a time-of-day was already set, so we know
// whether to default the minute (and hour) for day-scoped phrases. For
// example, "every monday" with no time defaults to 00:00 (midnight) — a sane,
// explicit choice rather than "every minute on Monday".
func applyRecurrence(phrase string, c *cron, recur string, timeApplied bool) (subHour bool, err error) {
	recur = strings.TrimSpace(recur)

	// Empty recurrence: only a time was given (e.g. "at 9am") → daily at that
	// time. minute/hour are already set by the caller.
	if recur == "" {
		if !timeApplied {
			return false, errf(phrase, "nothing to schedule")
		}
		return false, nil
	}

	// Strip a leading "every " for the period/day forms ("every day",
	// "every 15 minutes", "every monday"). "each" is treated as a synonym.
	body := recur
	for _, pre := range []string{"every ", "each ", "on ", "on the ", "the "} {
		if strings.HasPrefix(body, pre) {
			body = strings.TrimSpace(body[len(pre):])
			break
		}
	}

	// --- bare period keywords -------------------------------------------------
	switch body {
	case "minute", "1 minute":
		c.minute, c.hour = "*", "*"
		return true, nil
	case "hour", "1 hour", "hourly":
		// On the hour. If a time was given, that's contradictory, but the caller
		// catches sub-hour conflicts only; here "every hour at 9am" is nonsense,
		// so guard it explicitly.
		if timeApplied {
			return false, errf(phrase, "\"every hour\" can't also have a fixed time")
		}
		c.minute, c.hour = "0", "*"
		return false, nil
	case "day", "daily":
		defaultTime(c, timeApplied)
		return false, nil
	case "weekday", "weekdays":
		c.dow = "1-5"
		defaultTime(c, timeApplied)
		return false, nil
	case "weekend", "weekends":
		c.dow = "0,6"
		defaultTime(c, timeApplied)
		return false, nil
	case "week", "weekly":
		// Weekly with no named day → Sunday, a common convention.
		c.dow = "0"
		defaultTime(c, timeApplied)
		return false, nil
	case "month", "monthly":
		c.dom = "1"
		defaultTime(c, timeApplied)
		return false, nil
	}

	// --- "every N minutes/hours" ---------------------------------------------
	if m := everyNUnitRE.FindStringSubmatch(body); m != nil {
		n, _ := strconv.Atoi(m[1])
		if n <= 0 {
			return false, errf(phrase, "interval must be positive")
		}
		unit := m[2]
		if strings.HasPrefix(unit, "min") {
			if n > 59 {
				return false, errf(phrase, "an every-%d-minutes interval exceeds 59; use hours instead", n)
			}
			c.minute, c.hour = "*/"+strconv.Itoa(n), "*"
			return true, nil
		}
		// hours
		if n > 23 {
			return false, errf(phrase, "an every-%d-hours interval exceeds 23; use a daily time instead", n)
		}
		c.hour = "*/" + strconv.Itoa(n)
		c.minute = "0" // top of the hour, every N hours
		return false, nil
	}

	// --- named weekday list: "monday", "tuesday and thursday", "mon,wed,fri" --
	if days, ok := parseWeekdayList(body); ok {
		c.dow = days
		defaultTime(c, timeApplied)
		return false, nil
	}

	// --- "Nth of the month" / "first of the month" ---------------------------
	if dom, ok := parseDayOfMonth(body); ok {
		if dom < 0 {
			return false, errf(phrase, "cron can't express \"last of the month\"; pick a specific day")
		}
		c.dom = strconv.Itoa(dom)
		defaultTime(c, timeApplied)
		return false, nil
	}

	// --- named month: "january", "every december" → first of that month ------
	if mon, ok := monthNum[singularize(body)]; ok {
		c.month = strconv.Itoa(mon)
		// "every January" most naturally means the 1st of January, not every
		// minute of January. Anchor to day 1 unless a finer recurrence said so.
		if c.dom == "*" && c.dow == "*" {
			c.dom = "1"
		}
		defaultTime(c, timeApplied)
		return false, nil
	}

	return false, errf(phrase, "unrecognized schedule %q", recur)
}

// defaultTime fills minute (and hour) for a day-scoped phrase when no explicit
// time was supplied. The convention: a day without a time means midnight
// (00:00), which is unambiguous and matches how people say "every Monday" to
// mean the start of Monday.
func defaultTime(c *cron, timeApplied bool) {
	if timeApplied {
		return
	}
	if c.minute == "*" {
		c.minute = "0"
	}
	if c.hour == "*" {
		c.hour = "0"
	}
}

// parseWeekdayList parses "monday", "monday and friday", "mon, wed and fri",
// "tuesday & thursday" into a cron day-of-week field. It returns ok=false if
// any token isn't a weekday, so the caller can try other rules.
func parseWeekdayList(s string) (field string, ok bool) {
	tokens := splitList(s)
	if len(tokens) == 0 {
		return "", false
	}
	nums := make([]string, 0, len(tokens))
	for _, t := range tokens {
		n, found := dowNum[t]
		if !found {
			// Accept a plural day name ("mondays") by trimming the trailing "s".
			if n, found = dowNum[singularize(t)]; !found {
				return "", false
			}
		}
		nums = append(nums, strconv.Itoa(n))
	}
	return strings.Join(nums, ","), true
}

// parseDayOfMonth parses "first", "the 15th", "1st", "15" into a day number.
// "last" maps to -1 so the caller can reject it with a clear message.
func parseDayOfMonth(s string) (day int, ok bool) {
	s = strings.TrimSpace(s)
	// Trim a trailing "of the month" / "of month" / "of every month".
	for _, suf := range []string{" of the month", " of every month", " of month", " day of the month"} {
		s = strings.TrimSuffix(s, suf)
	}
	s = strings.TrimSpace(s)

	if n, found := ordinalWord[s]; found {
		return n, true
	}
	if m := nthOfMonthRE.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		if n >= 1 && n <= 31 {
			return n, true
		}
	}
	return 0, false
}

// splitList breaks a human list on commas, "and", and "&", trimming each item.
// "monday, wednesday and friday" → ["monday","wednesday","friday"].
func splitList(s string) []string {
	// Normalize separators to commas first.
	s = strings.ReplaceAll(s, " and ", ",")
	s = strings.ReplaceAll(s, "&", ",")
	raw := strings.Split(s, ",")
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		r = strings.TrimSpace(r)
		if r != "" {
			out = append(out, r)
		}
	}
	return out
}

// singularize trims a trailing plural "s" for month matching ("januarys" is
// nobody's intent, but cheap insurance and keeps the map keys singular).
func singularize(s string) string {
	if len(s) > 1 && strings.HasSuffix(s, "s") {
		return s[:len(s)-1]
	}
	return s
}
