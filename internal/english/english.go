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
//	"every morning"                     -> 0 6 * * *
//	"every night"                       -> 0 21 * * *
//	"every weekday evening"             -> 0 18 * * 1-5
//	"once a day"                        -> 0 0 * * *
//	"twice a day"                       -> 0 0,12 * * *
//	"every day at 9am and 5pm"          -> 0 9,17 * * *
//	"every 3 days"                      -> 0 0 */3 * *
//	"every other day"                   -> 0 0 */2 * *
//	"quarterly"                         -> 0 0 1 1,4,7,10 *
//	"yearly"                            -> 0 0 1 1 *
//
// Anything outside this grammar returns an error (with the goblin's blessing
// to grumble elsewhere) rather than guessing — a wrong cron line is worse than
// an honest "I didn't understand that".
package english

import (
	"fmt"
	"regexp"
	"sort"
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

// namedTime maps fuzzy times-of-day people actually say to a concrete
// (hour, minute). These are conventions, deliberately chosen once so output
// stays deterministic: "morning" is 6am, "noon"/"midday" 12pm, "afternoon"
// 12pm as well (early afternoon), "evening" 6pm, "night" 9pm, "midnight" 0.
// They work both as a bare recurrence ("every morning") and as a time clause
// ("every weekday at night").
var namedTime = map[string]struct{ hour, minute int }{
	"midnight":  {0, 0},
	"morning":   {6, 0},
	"noon":      {12, 0},
	"midday":    {12, 0},
	"afternoon": {12, 0},
	"evening":   {18, 0},
	"night":     {21, 0},
	"tonight":   {21, 0},
	"lunchtime": {12, 0},
	"noontime":  {12, 0},
}

// countPerPeriod maps "once"/"twice"/... to how many evenly spaced fires per
// period the phrase implies (used by "twice a day", "once an hour").
var countPerPeriod = map[string]int{
	"once": 1, "one time": 1, "1 time": 1,
	"twice": 2, "two times": 2, "2 times": 2,
	"thrice": 3, "three times": 3, "3 times": 3,
	"four times": 4, "4 times": 4,
}

// timeRE matches an explicit clock time: "9", "9am", "9:30", "6:30pm",
// "12:00 am". Hour is required; minutes and the am/pm marker are optional.
var timeRE = regexp.MustCompile(`^(\d{1,2})(?::(\d{2}))?\s*(am|pm)?$`)

// everyNUnitRE matches "every 15 minutes" / "every 2 hours" (unit may be
// singular or plural). The leading "every " is stripped before matching.
var everyNUnitRE = regexp.MustCompile(`^(\d+)\s+(minute|minutes|min|mins|hour|hours|hr|hrs)$`)

// everyNDaysRE matches "3 days" / "2 days" (the "every " prefix is already
// stripped by the caller). It drives a day-of-month step, e.g. */3.
var everyNDaysRE = regexp.MustCompile(`^(\d+)\s+(?:day|days)$`)

// everyNMonthsRE matches "2 months" / "3 months" → a month step (*/N).
var everyNMonthsRE = regexp.MustCompile(`^(\d+)\s+(?:month|months)$`)

// everyNWeeksRE matches "2 weeks" / "3 weeks". Cron has no true multi-week
// period, so these are rejected with a clear message rather than approximated.
var everyNWeeksRE = regexp.MustCompile(`^(\d+)\s+(?:week|weeks)$`)

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

	// 1) Apply the time-of-day clause, if present. This sets minute+hour and
	// supports a small list of times ("9am and 5pm") that share a minute.
	timeApplied := false
	if timeStr != "" {
		minField, hourField, err := parseTimeClause(phrase, timeStr)
		if err != nil {
			return "", err
		}
		c.minute, c.hour = minField, hourField
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
//
// It also recognizes a trailing named time-of-day with no "at" — the way
// people actually talk: "every morning", "every weekday evening",
// "weekends at night". The named word is peeled off as the time clause and the
// remainder (if any) stays the recurrence.
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
		// A trailing named time-of-day without "at": "every morning",
		// "every weekday evening". Peel it off so the recurrence can be scoped
		// independently. "in the morning" is normalized to "morning" first.
		if r, t, ok := splitTrailingNamedTime(s); ok {
			return r, t
		}
		return s, ""
	}
	recur = strings.TrimSpace(s[:idx])
	timeStr = strings.TrimSpace(s[idx+len(" at "):])
	return recur, timeStr
}

// splitTrailingNamedTime detects a phrase ending in a named time-of-day
// ("morning", "evening", "night", ...), optionally preceded by "in the",
// returning the leading recurrence and the bare time word. "every morning"
// yields ("", "morning"); "every weekday evening" yields ("every weekday",
// "evening").
func splitTrailingNamedTime(s string) (recur, timeStr string, ok bool) {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return "", "", false
	}
	last := fields[len(fields)-1]
	if _, isNamed := namedTime[last]; !isNamed {
		return "", "", false
	}
	rest := strings.TrimSpace(strings.TrimSuffix(s, last))
	// Drop a connective "in the" / "in" that often precedes the time word.
	rest = strings.TrimSuffix(rest, " in the")
	rest = strings.TrimSuffix(rest, " in")
	rest = strings.TrimSpace(rest)
	// Bare "morning"/"evening" alone (optionally "in the morning") means daily
	// at that time.
	switch rest {
	case "", "every", "each", "in the", "in", "the":
		return "", last, true
	}
	return rest, last, true
}

// parseTimeOfDay parses a time clause into minute and hour (24h). It accepts
// "noon", "midnight", other named times-of-day ("morning", "evening",
// "night"), and clock forms like "9", "9am", "6:30pm", "14:00".
func parseTimeOfDay(phrase, timeStr string) (minute, hour int, err error) {
	if nt, ok := namedTime[timeStr]; ok {
		return nt.minute, nt.hour, nil
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

// parseTimeClause parses one or more times-of-day sharing a single minute into
// cron minute/hour fields. A single time ("6:30pm") yields ("30","18"); a list
// ("9am and 5pm", "9am, noon and 5pm") yields ("0","9,12,17"). Because cron has
// exactly one minute field, every listed time must share the same minute — a
// mixed-minute list like "9:15am and 5:45pm" is rejected with a clear message
// rather than silently dropping precision. Hours are emitted sorted and
// de-duplicated for a stable, canonical expression.
func parseTimeClause(phrase, timeStr string) (minuteField, hourField string, err error) {
	parts := splitList(timeStr)
	if len(parts) <= 1 {
		min, hr, e := parseTimeOfDay(phrase, timeStr)
		if e != nil {
			return "", "", e
		}
		return strconv.Itoa(min), strconv.Itoa(hr), nil
	}

	var sharedMin = -1
	hours := make([]int, 0, len(parts))
	seen := make(map[int]bool, len(parts))
	for _, p := range parts {
		min, hr, e := parseTimeOfDay(phrase, p)
		if e != nil {
			return "", "", e
		}
		if sharedMin == -1 {
			sharedMin = min
		} else if min != sharedMin {
			return "", "", errf(phrase, "a list of times must share the same minute (got :%02d and :%02d); cron has only one minute field", sharedMin, min)
		}
		if !seen[hr] {
			seen[hr] = true
			hours = append(hours, hr)
		}
	}
	sort.Ints(hours)
	strs := make([]string, len(hours))
	for i, h := range hours {
		strs[i] = strconv.Itoa(h)
	}
	return strconv.Itoa(sharedMin), strings.Join(strs, ","), nil
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

	// --- "once/twice a day", "once an hour" ----------------------------------
	// These read on the raw recurrence (no "every " to strip). "a"/"an"/"per"
	// are all accepted as the connective.
	if n, period, ok := parseCountPerPeriod(recur); ok {
		switch period {
		case "day":
			if timeApplied {
				return false, errf(phrase, "%q already implies its own times; drop the \"at ...\"", recur)
			}
			c.minute = "0"
			c.hour = evenlySpaced(n, 24)
			return false, nil
		case "hour":
			if timeApplied {
				return false, errf(phrase, "%q already implies its own minutes; drop the \"at ...\"", recur)
			}
			c.minute = evenlySpaced(n, 60)
			c.hour = "*"
			return n > 1, nil // more-than-once-an-hour is sub-hourly
		}
	}

	// "every other X" means every 2nd X. Rewrite to the numeric form so the
	// rules below handle it uniformly ("every other day" -> "every 2 days").
	if rest, ok := strings.CutPrefix(recur, "every other "); ok {
		recur = "every 2 " + pluralizeUnit(rest)
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
	case "quarter", "quarterly":
		// First of each calendar quarter: Jan, Apr, Jul, Oct.
		c.dom, c.month = "1", "1,4,7,10"
		defaultTime(c, timeApplied)
		return false, nil
	case "year", "yearly", "annually", "annual":
		// New Year's: first minute of Jan 1.
		c.dom, c.month = "1", "1"
		defaultTime(c, timeApplied)
		return false, nil
	case "biweekly", "bi-weekly", "fortnightly", "fortnight":
		return false, errf(phrase, "cron can't express a bi-weekly/fortnightly cadence; pick specific days or a day-of-month")
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

	// --- "every N days" ------------------------------------------------------
	// Cron can't truly do "every N days" across month boundaries; the honest
	// approximation is a day-of-month step (*/N), which resets on the 1st. We
	// accept it for the common small intervals and let the readback/docs make
	// the caveat visible rather than refusing a very common phrase.
	if m := everyNDaysRE.FindStringSubmatch(body); m != nil {
		n, _ := strconv.Atoi(m[1])
		if n <= 0 {
			return false, errf(phrase, "interval must be positive")
		}
		if n == 1 {
			defaultTime(c, timeApplied)
			return false, nil // "every 1 day" == daily
		}
		if n > 31 {
			return false, errf(phrase, "an every-%d-days interval exceeds a month; use a monthly or dated schedule", n)
		}
		c.dom = "*/" + strconv.Itoa(n)
		defaultTime(c, timeApplied)
		return false, nil
	}

	// --- "every N months" ----------------------------------------------------
	if m := everyNMonthsRE.FindStringSubmatch(body); m != nil {
		n, _ := strconv.Atoi(m[1])
		if n <= 0 {
			return false, errf(phrase, "interval must be positive")
		}
		if n > 12 {
			return false, errf(phrase, "an every-%d-months interval exceeds a year", n)
		}
		if c.dom == "*" {
			c.dom = "1" // anchor to the 1st so it fires once per stepped month
		}
		if n == 1 {
			defaultTime(c, timeApplied)
			return false, nil // "every 1 month" == monthly
		}
		c.month = "*/" + strconv.Itoa(n)
		defaultTime(c, timeApplied)
		return false, nil
	}

	// --- "every N weeks" (incl. bi-weekly) -----------------------------------
	// Cron cannot express a true multi-week cadence (a day-of-week step lands on
	// several weekdays within the same week, not "every other Monday"). Refuse
	// honestly instead of emitting something subtly wrong.
	if everyNWeeksRE.MatchString(body) {
		return false, errf(phrase, "cron can't express a multi-week cadence (bi-weekly/fortnightly); pick specific days or a day-of-month")
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

// pluralizeUnit ensures a bare unit reads as a plural for the "every N units"
// rules ("day" -> "days"), so "every other day" can be rewritten to
// "every 2 days" and reuse the numeric path.
func pluralizeUnit(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasSuffix(s, "s") {
		return s
	}
	return s + "s"
}

// parseCountPerPeriod recognizes "once a day", "twice a day", "once an hour",
// "3 times a day", etc. It returns the fire count, the period ("day"/"hour"),
// and ok. The connective may be "a", "an", or "per".
func parseCountPerPeriod(recur string) (count int, period string, ok bool) {
	s := recur
	var rest string
	for _, sep := range []string{" a ", " an ", " per "} {
		if i := strings.Index(s, sep); i >= 0 {
			countPart := strings.TrimSpace(s[:i])
			rest = strings.TrimSpace(s[i+len(sep):])
			n, found := countPerPeriod[countPart]
			if !found {
				return 0, "", false
			}
			switch singularize(rest) {
			case "day":
				return n, "day", true
			case "hour":
				return n, "hour", true
			default:
				return 0, "", false
			}
		}
	}
	return 0, "", false
}

// evenlySpaced returns a cron field listing n values spread as evenly as
// possible across a period of the given size starting at 0. For n==1 it is
// "0"; for divisors of size it uses the step form ("*/12" style is avoided in
// favor of an explicit list so the output reads unambiguously). Example:
// evenlySpaced(2, 24) -> "0,12"; evenlySpaced(2, 60) -> "0,30".
func evenlySpaced(n, size int) string {
	if n <= 1 {
		return "0"
	}
	if n >= size {
		return "*"
	}
	step := size / n
	vals := make([]string, 0, n)
	for i := 0; i < n; i++ {
		vals = append(vals, strconv.Itoa(i*step))
	}
	return strings.Join(vals, ",")
}
