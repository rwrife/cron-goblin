// systemd.go implements the systemd OnCalendar -> standard 5-field cron slice of
// the dialect adapter. systemd.timer units schedule work with an OnCalendar=
// expression whose grammar is close to, but noticeably richer than, cron. This
// file translates the losslessly-representable subset into standard cron and
// returns a specific *ConvertError (with the same "convert or honestly refuse"
// contract as FromQuartz) for anything cron cannot carry.
//
// # OnCalendar grammar (the parts we handle)
//
// A full expression is up to three space-separated components:
//
//	[DayOfWeek] [Year-Month-Day] [Hour:Minute[:Second]]
//
// Any component may be omitted (systemd fills the gap with "*"). Within each
// numeric position we accept the forms cron itself can represent:
//
//   - "*"        every value
//   - "5"        a single value
//   - "1..5"     an inclusive range (systemd spells ranges with "..")
//   - "*/2"      a repetition/step across the whole range
//   - "1..5/2"   a stepped range
//   - "0,15,30"  a comma list of any of the above
//
// Weekdays use English abbreviations (Mon, Tue, ... Sun), single values,
// comma lists, and ".." ranges (Mon..Fri). systemd weekday names map straight
// onto cron's named weekdays.
//
// # Named shorthands
//
// systemd defines convenience aliases (minutely, hourly, daily, weekly,
// monthly, quarterly, semiannually, yearly/annually). The ones that have an
// exact 5-field cron equivalent are expanded; the sub-minute one (minutely is
// fine, but there is no "secondly") and any alias that would need a seconds
// field are handled honestly.
//
// # What cron cannot carry (refused, not guessed)
//
//   - a seconds value other than 0 (cron has no seconds field)
//   - a specific year or year range (cron has no year field)
//   - the "~" last-day-of-month marker and other systemd-only specials
//
// These raise a *ConvertError so `goblin convert` can point the user at the
// exact field instead of silently emitting a wrong schedule.
package dialect

import (
	"fmt"
	"strconv"
	"strings"
)

// systemdDOWNames maps systemd weekday abbreviations to their canonical cron
// name. systemd and cron share the same three-letter English abbreviations, so
// this is mostly an accept-list; it also tolerates the full names systemd
// permits (Monday, Tuesday, ...).
var systemdDOWNames = map[string]string{
	"MON": "MON", "MONDAY": "MON",
	"TUE": "TUE", "TUESDAY": "TUE",
	"WED": "WED", "WEDNESDAY": "WED",
	"THU": "THU", "THURSDAY": "THU",
	"FRI": "FRI", "FRIDAY": "FRI",
	"SAT": "SAT", "SATURDAY": "SAT",
	"SUN": "SUN", "SUNDAY": "SUN",
}

// systemdShorthands maps systemd's named calendar events to the equivalent
// standard 5-field cron expression. Only aliases with an exact cron equivalent
// live here; see FromSystemd for the ones that must be refused.
//
// Reference (systemd.time):
//
//	minutely  -> *:*:00        -> every minute
//	hourly    -> *:00:00       -> minute 0 of every hour
//	daily     -> 00:00:00      -> midnight every day
//	weekly    -> Mon 00:00:00  -> midnight every Monday
//	monthly   -> *-*-01 00:00  -> midnight on the 1st
//	yearly    -> *-01-01 00:00 -> midnight on Jan 1
//	quarterly -> *-01,04,07,10-01 00:00
var systemdShorthands = map[string]string{
	"minutely":  "* * * * *",
	"hourly":    "0 * * * *",
	"daily":     "0 0 * * *",
	"weekly":    "0 0 * * MON",
	"monthly":   "0 0 1 * *",
	"yearly":    "0 0 1 1 *",
	"annually":  "0 0 1 1 *",
	"quarterly": "0 0 1 1,4,7,10 *",
}

// isAlphaToken reports whether every rune in s is an ASCII letter. It gates the
// "unknown shorthand" diagnostic so that a lone non-alphabetic token (like "*")
// is not misreported as a mistyped alias.
func isAlphaToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			return false
		}
	}
	return true
}

// FromSystemd converts a systemd OnCalendar expression into the equivalent
// standard 5-field cron string. Like FromQuartz, it focuses on the dialect-shape
// translation and leaves final range validation to parse.Parse, so callers
// should still parse the result. It returns a *ConvertError when the source is
// valid systemd but not representable in standard cron (Lossy=true) or when the
// input is malformed / uses an unsupported systemd-only construct (Lossy=false).
func FromSystemd(expr string) (string, error) {
	trimmed := strings.TrimSpace(expr)
	if trimmed == "" {
		return "", &ConvertError{Dialect: Systemd, Msg: "empty expression"}
	}

	// Named shorthands first: a single bare word (no time ":" or date "-"
	// punctuation) that matches a known alias. A lone token that *does* carry
	// ":" or "-" is a real one-component expression (e.g. "09:00" or "*-*-01")
	// and must fall through to the component parser below.
	if !strings.ContainsAny(trimmed, " \t:-") {
		lower := strings.ToLower(trimmed)
		if cron, ok := systemdShorthands[lower]; ok {
			return cron, nil
		}
		// "semiannually"/"biannually" fire twice a year but on Jan 1 and Jul 1,
		// which *is* expressible; systemd defines it as *-01,07-01. Handle it
		// alongside the table so the message below stays about true unknowns.
		if lower == "semiannually" || lower == "biannually" {
			return "0 0 1 1,7 *", nil
		}
		// A lone alphabetic token that is neither a recognized alias nor a
		// weekday name is almost certainly a typo'd shorthand; say so rather than
		// trying to parse it as a component. A bare weekday (e.g. "Mon") is a
		// valid one-component OnCalendar meaning "every Monday at 00:00", so let
		// it fall through to the component parser. ("*" is not alphabetic and is
		// handled below.)
		if _, isDay := systemdDOWNames[strings.ToUpper(trimmed)]; isAlphaToken(trimmed) && !isDay {
			return "", &ConvertError{
				Dialect: Systemd,
				Msg: fmt.Sprintf("unknown OnCalendar shorthand %q (try: minutely, hourly, daily, weekly, monthly, quarterly, yearly)",
					trimmed),
			}
		}
	}

	dow, date, clock, err := splitSystemdComponents(trimmed)
	if err != nil {
		return "", err
	}

	minute, hour, err := convertSystemdClock(clock)
	if err != nil {
		return "", err
	}

	dom, month, err := convertSystemdDate(date)
	if err != nil {
		return "", err
	}

	cronDOW, err := convertSystemdDOW(dow)
	if err != nil {
		return "", err
	}

	return strings.Join([]string{minute, hour, dom, month, cronDOW}, " "), nil
}

// splitSystemdComponents breaks an OnCalendar expression into its optional
// day-of-week, date (Year-Month-Day), and clock (Hour:Minute:Second) parts.
// systemd allows omitting the leading day-of-week and/or the date, deciding
// which component a token is by its shape: a token containing ":" is the clock,
// a token containing "-" is the date, and anything else is the weekday list.
// Each component may appear at most once.
func splitSystemdComponents(expr string) (dow, date, clock string, err error) {
	for _, tok := range strings.Fields(expr) {
		switch {
		case strings.Contains(tok, ":"):
			if clock != "" {
				return "", "", "", &ConvertError{Dialect: Systemd, Msg: "more than one time (H:M:S) component"}
			}
			clock = tok
		case strings.Contains(tok, "-"):
			if date != "" {
				return "", "", "", &ConvertError{Dialect: Systemd, Msg: "more than one date (Y-M-D) component"}
			}
			date = tok
		default:
			// A bare token with neither ":" nor "-" is the weekday component. It
			// may be a comma list (Mon,Wed) but systemd writes weekday *ranges*
			// with "..", which contains no "-", so it still lands here.
			if dow != "" {
				return "", "", "", &ConvertError{Dialect: Systemd, Msg: "more than one day-of-week component"}
			}
			dow = tok
		}
	}
	return dow, date, clock, nil
}

// convertSystemdClock turns an "H:M[:S]" systemd time component into cron
// minute and hour fields. A missing clock (empty string) means "every minute"
// in cron terms is *not* right — systemd treats an omitted time as 00:00:00 for
// calendar events — so callers pass the whole expression's clock through here
// and an empty clock maps to midnight (minute 0, hour 0), matching systemd.
func convertSystemdClock(clock string) (minute, hour string, err error) {
	if clock == "" {
		// systemd: when the time is omitted it defaults to 00:00:00.
		return "0", "0", nil
	}

	parts := strings.Split(clock, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return "", "", &ConvertError{
			Dialect: Systemd,
			Field:   "time",
			Msg:     fmt.Sprintf("expected H:M or H:M:S, got %q", clock),
		}
	}

	// Seconds, when present, must be exactly 0 (or "*" meaning every second is
	// sub-minute precision and refused). cron has no seconds field.
	if len(parts) == 3 {
		sec := strings.TrimSpace(parts[2])
		if sec != "0" && sec != "00" {
			return "", "", &ConvertError{
				Dialect: Systemd,
				Field:   "seconds",
				Msg:     fmt.Sprintf("standard cron has no seconds field; only \"0\" converts losslessly, got %q", sec),
				Lossy:   true,
			}
		}
	}

	hourField, err := convertSystemdNumeric(parts[0], "hour")
	if err != nil {
		return "", "", err
	}
	minuteField, err := convertSystemdNumeric(parts[1], "minute")
	if err != nil {
		return "", "", err
	}
	return minuteField, hourField, nil
}

// convertSystemdDate turns a "Y-M-D" systemd date component into cron
// day-of-month and month fields. A missing date maps to "* *" (any day, any
// month). The year, if given, must be unrestricted ("*"); a specific year is
// refused because cron has no year field.
func convertSystemdDate(date string) (dom, month string, err error) {
	if date == "" {
		return "*", "*", nil
	}

	parts := strings.Split(date, "-")
	// systemd accepts "M-D" (year omitted) or "Y-M-D".
	var yearPart, monthPart, dayPart string
	switch len(parts) {
	case 2:
		yearPart, monthPart, dayPart = "*", parts[0], parts[1]
	case 3:
		yearPart, monthPart, dayPart = parts[0], parts[1], parts[2]
	default:
		return "", "", &ConvertError{
			Dialect: Systemd,
			Field:   "date",
			Msg:     fmt.Sprintf("expected Y-M-D or M-D, got %q", date),
		}
	}

	if y := strings.TrimSpace(yearPart); y != "*" && y != "" {
		return "", "", &ConvertError{
			Dialect: Systemd,
			Field:   "year",
			Msg:     fmt.Sprintf("standard cron has no year field; %q would be lost", y),
			Lossy:   true,
		}
	}

	// The systemd "~" marker counts back from the last day of the month
	// (e.g. ~3 = third-to-last day). cron cannot express it.
	if strings.Contains(dayPart, "~") {
		return "", "", &ConvertError{
			Dialect: Systemd,
			Field:   "day-of-month",
			Msg:     "systemd \"~\" (days from month end) has no standard-cron equivalent",
		}
	}

	monthField, err := convertSystemdNumeric(monthPart, "month")
	if err != nil {
		return "", "", err
	}
	dayField, err := convertSystemdNumeric(dayPart, "day-of-month")
	if err != nil {
		return "", "", err
	}
	return dayField, monthField, nil
}

// convertSystemdDOW turns a systemd weekday component (empty, or names/lists/
// ".." ranges like "Mon..Fri" or "Mon,Wed,Fri") into a cron day-of-week field.
//
// systemd orders weekdays Mon(1)..Sun(7); cron's *names* are unordered text but
// its numbers run Sun(0)..Sat(6). To stay correct regardless of that numbering
// gap, ranges are expanded into an explicit comma list of cron weekday *names*
// using systemd's ordering. This makes ranges that would "wrap" in cron numbers
// (e.g. Fri..Sun -> 5-0, which a numeric cron parser rejects as inverted) come
// out as an unambiguous FRI,SAT,SUN instead of an invalid or wrong expression.
func convertSystemdDOW(dow string) (string, error) {
	if dow == "" {
		return "*", nil
	}
	if dow == "*" {
		return "*", nil
	}

	terms := strings.Split(dow, ",")
	out := make([]string, 0, len(terms))
	for _, term := range terms {
		term = strings.TrimSpace(term)
		if term == "" {
			return "", &ConvertError{Dialect: Systemd, Field: "day-of-week", Msg: "empty term in comma list"}
		}
		// systemd ranges use "..", e.g. Mon..Fri. Expand into an explicit list of
		// cron day names over systemd's Mon(1)..Sun(7) ordering.
		if idx := strings.Index(term, ".."); idx >= 0 {
			expanded, err := expandSystemdDOWRange(term[:idx], term[idx+2:])
			if err != nil {
				return "", err
			}
			out = append(out, expanded...)
			continue
		}
		name, err := normalizeSystemdDOWName(term)
		if err != nil {
			return "", err
		}
		out = append(out, name)
	}
	return strings.Join(out, ","), nil
}

// systemdWeekOrder is the canonical Mon..Sun ordering systemd uses for weekday
// ranges, expressed as the cron day names this package emits.
var systemdWeekOrder = []string{"MON", "TUE", "WED", "THU", "FRI", "SAT", "SUN"}

// systemdWeekIndex is the position (0=Mon .. 6=Sun) of each cron day name within
// systemdWeekOrder, for resolving range endpoints.
var systemdWeekIndex = map[string]int{
	"MON": 0, "TUE": 1, "WED": 2, "THU": 3, "FRI": 4, "SAT": 5, "SUN": 6,
}

// expandSystemdDOWRange expands a systemd weekday range "lo..hi" (each endpoint
// a name or 1-7 number) into the inclusive list of cron day names between them,
// walking systemd's Mon..Sun order. It refuses an inverted range (lo after hi)
// rather than silently wrapping, matching systemd, which also treats the low
// endpoint as needing to precede the high one.
func expandSystemdDOWRange(loTok, hiTok string) ([]string, error) {
	lo, err := normalizeSystemdDOWName(loTok)
	if err != nil {
		return nil, err
	}
	hi, err := normalizeSystemdDOWName(hiTok)
	if err != nil {
		return nil, err
	}
	loIdx := systemdWeekIndex[lo]
	hiIdx := systemdWeekIndex[hi]
	if loIdx > hiIdx {
		return nil, &ConvertError{
			Dialect: Systemd,
			Field:   "day-of-week",
			Msg:     fmt.Sprintf("range %s..%s is inverted (systemd weekdays run Mon..Sun)", lo, hi),
		}
	}
	days := make([]string, 0, hiIdx-loIdx+1)
	for i := loIdx; i <= hiIdx; i++ {
		days = append(days, systemdWeekOrder[i])
	}
	return days, nil
}

// normalizeSystemdDOWName resolves a single systemd weekday token (short or long
// name) to its canonical three-letter cron name. Numeric weekdays are also
// accepted: systemd numbers weekdays 1-7 as Mon-Sun, so they are translated to
// the matching cron name rather than passed through (cron's 0-6 is Sun-Sat, a
// different numbering, so emitting the name avoids an off-by-one).
func normalizeSystemdDOWName(tok string) (string, error) {
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return "", &ConvertError{Dialect: Systemd, Field: "day-of-week", Msg: "empty weekday"}
	}
	upper := strings.ToUpper(tok)
	if name, ok := systemdDOWNames[upper]; ok {
		return name, nil
	}
	// systemd also allows numeric weekdays 1-7 (Mon-Sun).
	if n, err := strconv.Atoi(tok); err == nil {
		switch {
		case n >= 1 && n <= 7:
			names := []string{"MON", "TUE", "WED", "THU", "FRI", "SAT", "SUN"}
			return names[n-1], nil
		default:
			return "", &ConvertError{
				Dialect: Systemd,
				Field:   "day-of-week",
				Msg:     fmt.Sprintf("%d is out of range (systemd weekdays are 1-7, Mon-Sun)", n),
			}
		}
	}
	return "", &ConvertError{
		Dialect: Systemd,
		Field:   "day-of-week",
		Msg:     fmt.Sprintf("%q is not a valid weekday (expected Mon-Sun or 1-7)", tok),
	}
}

// convertSystemdNumeric translates one systemd numeric field (used for hour,
// minute, month, and day-of-month) into the cron spelling. The two dialects
// share "*", single values, comma lists, and "/step"; the only spelling
// difference is the range separator ("a..b" in systemd, "a-b" in cron), which
// this rewrites. Values are otherwise passed through untouched and validated
// later by parse.Parse against the field's real bounds.
//
// Leading zeros (systemd commonly writes 08:05) are stripped from bare numbers
// so the cron parser, which does strconv.Atoi, sees canonical digits — Atoi
// actually accepts "08", but normalizing keeps the emitted cron tidy.
func convertSystemdNumeric(field, name string) (string, error) {
	field = strings.TrimSpace(field)
	if field == "" {
		return "", &ConvertError{
			Dialect: Systemd,
			Field:   name,
			Msg:     "empty value",
		}
	}
	if field == "*" {
		return "*", nil
	}

	terms := strings.Split(field, ",")
	out := make([]string, 0, len(terms))
	for _, term := range terms {
		term = strings.TrimSpace(term)
		if term == "" {
			return "", &ConvertError{Dialect: Systemd, Field: name, Msg: "empty term in comma list"}
		}

		// Split off an optional "/step"; the step is a count carried through.
		body := term
		step := ""
		if slash := strings.IndexByte(term, '/'); slash >= 0 {
			body = term[:slash]
			step = term[slash+1:]
		}

		var rewritten string
		if idx := strings.Index(body, ".."); idx >= 0 {
			lo, err := canonicalSystemdNumber(body[:idx], name)
			if err != nil {
				return "", err
			}
			hi, err := canonicalSystemdNumber(body[idx+2:], name)
			if err != nil {
				return "", err
			}
			rewritten = lo + "-" + hi
		} else if body == "*" {
			rewritten = "*"
		} else {
			n, err := canonicalSystemdNumber(body, name)
			if err != nil {
				return "", err
			}
			rewritten = n
		}

		if step != "" {
			if _, err := strconv.Atoi(step); err != nil {
				return "", &ConvertError{
					Dialect: Systemd,
					Field:   name,
					Msg:     fmt.Sprintf("step %q is not a number in %q", step, term),
				}
			}
			rewritten += "/" + step
		}
		out = append(out, rewritten)
	}
	return strings.Join(out, ","), nil
}

// canonicalSystemdNumber validates that a bare systemd field token is numeric
// and returns it without leading zeros. Named months are not part of systemd's
// OnCalendar numeric grammar (it uses numbers 1-12), so a non-numeric token
// here is an error with the offending field named.
func canonicalSystemdNumber(tok, name string) (string, error) {
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return "", &ConvertError{Dialect: Systemd, Field: name, Msg: "empty value"}
	}
	n, err := strconv.Atoi(tok)
	if err != nil {
		return "", &ConvertError{
			Dialect: Systemd,
			Field:   name,
			Msg:     fmt.Sprintf("%q is not a number", tok),
		}
	}
	return strconv.Itoa(n), nil
}
