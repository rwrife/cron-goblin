// to_quartz.go implements the reverse direction of the dialect adapter:
// standard 5-field cron -> Quartz. Together with FromQuartz this closes the
// "Quartz <-> standard cron where possible" loop — you can now hand the goblin
// a plain crontab line and get back the Quartz spec a Spring/Elasticsearch job
// wants, not just the other way around.
//
//	goblin convert --from cron --to quartz "0 9 * * MON-FRI"  -> 0 0 9 ? * MON-FRI
//	goblin convert --from cron --to quartz "30 2 * * *"       -> 0 30 2 * * ?
//	goblin convert --from cron --to quartz "15 10 1 * *"      -> 0 15 10 1 * ?
//
// Standard 5-field cron is a near-subset of Quartz, so this direction is
// lossless in almost every case. Two things have to be handled rather than
// copied:
//
//   - Seconds. Quartz is 6-field (seconds first). Standard cron always fires at
//     the top of the minute, so we prepend a literal "0" seconds field.
//   - The day fields. Quartz forbids specifying *both* day-of-month and
//     day-of-week: exactly one must be the "no specific value" marker `?`. Cron
//     has no `?` and applies its own DOM-or-DOW rule. We translate a `*` day
//     field to `?`, and refuse (with a clear message) the one shape Quartz
//     genuinely cannot express: a schedule that restricts DOM *and* DOW at once.
//   - Weekday numbering. Cron numbers weekdays 0-6 (SUN-SAT); Quartz numbers
//     them 1-7 (SUN-SAT). Numeric day-of-week tokens are shifted up by one;
//     shared SUN..SAT names pass through unchanged.
package dialect

import (
	"fmt"
	"strconv"
	"strings"
)

// cronDOWNames is the set of three-letter weekday abbreviations standard cron
// accepts. They are shared verbatim with Quartz, so a named day-of-week token
// converts by passing straight through (only numeric tokens get renumbered).
var cronDOWNames = map[string]struct{}{
	"SUN": {}, "MON": {}, "TUE": {}, "WED": {}, "THU": {}, "FRI": {}, "SAT": {},
}

// ToQuartz converts a standard 5-field cron expression into the equivalent
// Quartz (6-field) expression. Callers should already have a shape-valid cron
// string (convert.go round-trips through parse.Parse first); ToQuartz focuses on
// the dialect translation, not full range validation.
//
// Lossless conversions:
//   - seconds                     -> a literal "0" seconds field is prepended.
//   - a `*` day-of-week            -> `?` (with day-of-month kept), matching
//     Quartz's requirement that exactly one day field be `?`.
//   - a `*` day-of-month (when DOW
//     is restricted)               -> `?`.
//   - cron weekdays 0-6 (SUN-SAT)  -> Quartz 1-7 (SUN-SAT); names unchanged.
//
// Refused (returns *ConvertError, Lossy) for the single shape Quartz cannot
// represent: a schedule that pins *both* day-of-month and day-of-week, because
// Quartz mandates a `?` in one of the two.
func ToQuartz(expr string) (string, error) {
	trimmed := strings.TrimSpace(expr)
	if trimmed == "" {
		return "", &ConvertError{Dialect: Quartz, Msg: "empty expression"}
	}

	fields := strings.Fields(trimmed)
	if len(fields) != 5 {
		return "", &ConvertError{
			Dialect: Quartz,
			Msg: fmt.Sprintf("expected a standard 5-field cron expression (min hour day-of-month month day-of-week), got %d fields",
				len(fields)),
		}
	}

	minute := fields[0]
	hour := fields[1]
	dom := fields[2]
	month := fields[3]
	dow := fields[4]

	// Quartz requires exactly one of day-of-month / day-of-week to be `?` ("no
	// specific value"); it rejects an expression that constrains both. Standard
	// cron has no `?` and instead treats a restriction in either field as an OR.
	// Map the day fields per that rule:
	//   - DOW is `*`            -> DOW becomes `?` (day-of-month drives it).
	//   - DOM is `*` (DOW set)  -> DOM becomes `?` (day-of-week drives it).
	//   - both restricted       -> not representable in Quartz; refuse.
	domIsStar := dom == "*"
	dowIsStar := dow == "*"

	switch {
	case dowIsStar:
		// Whether or not day-of-month is `*`, blanking day-of-week to `?`
		// preserves the schedule (DOM, possibly `*`, is authoritative).
		dow = "?"
	case domIsStar:
		// Day-of-week is restricted and day-of-month is unrestricted: hand the
		// day decision to day-of-week and mark day-of-month `?`.
		dom = "?"
	default:
		// Both day-of-month and day-of-week are restricted. Standard cron ORs the
		// two; Quartz cannot express that (it forbids specifying both), so refuse
		// honestly rather than silently dropping one.
		return "", &ConvertError{
			Dialect: Quartz,
			Field:   "day-of-month/day-of-week",
			Msg: "cron restricts both day-of-month and day-of-week (an OR), " +
				"which Quartz cannot express — Quartz requires `?` in exactly one of the two",
			Lossy: true,
		}
	}

	// Renumber a restricted day-of-week from cron (0-6 SUN-SAT) to Quartz
	// (1-7 SUN-SAT). A `?` (just set above) or `*` needs no change.
	remappedDOW, err := remapCronDOWToQuartz(dow)
	if err != nil {
		return "", err
	}

	// Quartz is seconds-first; standard cron fires at second 0.
	return strings.Join([]string{"0", minute, hour, dom, month, remappedDOW}, " "), nil
}

// remapCronDOWToQuartz rewrites a standard-cron day-of-week field into Quartz
// numbering. cron numbers weekdays 0-6 (SUN-SAT); Quartz numbers them 1-7
// (SUN-SAT). The field may hold "*"/"?", lists (","), ranges ("-"), and steps
// ("/"); every numeric token is shifted up by one while shared SUN..SAT names
// pass through unchanged.
func remapCronDOWToQuartz(text string) (string, error) {
	if text == "*" || text == "?" {
		return text, nil
	}

	terms := strings.Split(text, ",")
	out := make([]string, 0, len(terms))
	for _, term := range terms {
		if term == "" {
			return "", &ConvertError{Dialect: Quartz, Field: "day-of-week", Msg: "empty term in comma list"}
		}

		// Peel off an optional "/step"; the step is a count, not a weekday, so it
		// is carried through unchanged.
		body := term
		step := ""
		if slash := strings.IndexByte(term, '/'); slash >= 0 {
			body = term[:slash]
			step = term[slash+1:]
		}

		var remappedBody string
		if dash := strings.IndexByte(body, '-'); dash >= 0 {
			lo, err := remapCronDOWTokenToQuartz(body[:dash])
			if err != nil {
				return "", err
			}
			hi, err := remapCronDOWTokenToQuartz(body[dash+1:])
			if err != nil {
				return "", err
			}
			remappedBody = fmt.Sprintf("%s-%s", lo, hi)
		} else {
			tok, err := remapCronDOWTokenToQuartz(body)
			if err != nil {
				return "", err
			}
			remappedBody = tok
		}

		if step != "" {
			out = append(out, remappedBody+"/"+step)
		} else {
			out = append(out, remappedBody)
		}
	}
	return strings.Join(out, ","), nil
}

// remapCronDOWTokenToQuartz remaps a single day-of-week token from cron (0-6
// SUN-SAT) to Quartz (1-7 SUN-SAT). A shared weekday name is returned unchanged
// (upper-cased); a number 0-6 is shifted up by one. cron also tolerates 7 as an
// alias for Sunday, which maps to Quartz's Sunday, 1.
func remapCronDOWTokenToQuartz(tok string) (string, error) {
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return "", &ConvertError{Dialect: Quartz, Field: "day-of-week", Msg: "empty value"}
	}
	if tok == "*" {
		return "*", nil
	}

	upper := strings.ToUpper(tok)
	if _, ok := cronDOWNames[upper]; ok {
		return upper, nil
	}

	n, err := strconv.Atoi(tok)
	if err != nil {
		return "", &ConvertError{
			Dialect: Quartz,
			Field:   "day-of-week",
			Msg:     fmt.Sprintf("%q is not a valid cron weekday (expected 0-7 or SUN-SAT)", tok),
		}
	}
	// cron weekdays are 0-6 (SUN-SAT); 7 is a lenient Sunday alias.
	if n < 0 || n > 7 {
		return "", &ConvertError{
			Dialect: Quartz,
			Field:   "day-of-week",
			Msg:     fmt.Sprintf("%d is out of range (cron weekdays are 0-6, with 7 as Sunday)", n),
		}
	}
	if n == 7 {
		// cron's Sunday alias -> Quartz Sunday (1).
		return "1", nil
	}
	// Shift 0-6 (SUN-SAT) up to 1-7 (SUN-SAT).
	return strconv.Itoa(n + 1), nil
}
