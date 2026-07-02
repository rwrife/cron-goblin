// Package dialect translates cron schedules between non-standard "dialects"
// and the standard 5-field cron that the rest of cron-goblin understands.
//
// The trusted core of cron-goblin (internal/parse) speaks exactly one language:
// standard 5-field cron (`min hour day-of-month month day-of-week`). Real-world
// schedules, though, show up in other formats — most commonly Quartz (the Java
// scheduler used by Spring, Elasticsearch, and friends), which adds a seconds
// field, an optional year, a `?` "no-specific-value" marker, and richer special
// characters (`L`, `W`, `#`). This package is the adapter layer: it converts
// what *can* be expressed in standard 5-field cron and returns a clear,
// specific error when a construct genuinely has no 5-field equivalent (a lossy
// or impossible conversion) rather than silently producing a wrong schedule.
//
// This first slice implements Quartz -> standard cron (see FromQuartz). Other
// dialects (Kubernetes CronJob, systemd OnCalendar) are tracked in the backlog
// and can plug in here behind the same "convert or honestly refuse" contract.
package dialect

import (
	"fmt"
	"strconv"
	"strings"
)

// Dialect identifies a cron "flavor" understood by this package. Only Quartz
// is implemented so far; the others are named for forward-looking CLI wiring
// and clearer "not yet supported" errors.
type Dialect string

const (
	// Cron is the standard 5-field Unix cron dialect (this tool's native tongue).
	Cron Dialect = "cron"
	// Quartz is the Java Quartz scheduler dialect: 6 or 7 fields
	// (seconds, minute, hour, day-of-month, month, day-of-week, [year]) with
	// `?`, `L`, `W`, and `#` special characters and 1-7 (SUN-SAT) weekdays.
	Quartz Dialect = "quartz"
	// Systemd is the systemd.timer OnCalendar dialect. Not yet implemented.
	Systemd Dialect = "systemd"
	// K8s is the Kubernetes CronJob schedule dialect. In practice it is standard
	// 5-field cron, so it is treated as an alias of Cron for validation.
	K8s Dialect = "k8s"
)

// ParseDialect resolves a case-insensitive dialect name (plus a few common
// aliases) to a Dialect. Unknown names return an error listing what is valid.
func ParseDialect(name string) (Dialect, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "cron", "unix", "standard", "5", "5-field":
		return Cron, nil
	case "quartz", "java":
		return Quartz, nil
	case "systemd", "oncalendar", "timer":
		return Systemd, nil
	case "k8s", "kubernetes", "cronjob":
		return K8s, nil
	default:
		return "", fmt.Errorf("unknown dialect %q (supported: cron, quartz, k8s, systemd)", name)
	}
}

// ConvertError describes a conversion that cannot be performed without changing
// the schedule's meaning. It names the offending Quartz field so the caller can
// point the user at the exact problem, and carries a Lossy flag distinguishing
// "impossible" (Lossy=false: malformed / unsupported dialect) from "lossy"
// (Lossy=true: valid Quartz whose semantics standard cron simply cannot carry,
// e.g. a seconds value or a specific year).
type ConvertError struct {
	Dialect Dialect // source dialect being converted from
	Field   string  // human name of the offending field, when applicable
	Msg     string  // explanation
	Lossy   bool    // true when the source is valid but not representable losslessly
}

func (e *ConvertError) Error() string {
	prefix := fmt.Sprintf("%s conversion", e.Dialect)
	if e.Field != "" {
		return fmt.Sprintf("%s: %s field: %s", prefix, e.Field, e.Msg)
	}
	return fmt.Sprintf("%s: %s", prefix, e.Msg)
}

// quartzDOWNames maps Quartz three-letter weekday abbreviations to the standard
// cron weekday number (Sunday = 0). Quartz and standard cron happen to use the
// same names, so only the numeric encodings differ (handled in remapQuartzDOW).
var quartzDOWNames = map[string]int{
	"SUN": 0, "MON": 1, "TUE": 2, "WED": 3, "THU": 4, "FRI": 5, "SAT": 6,
}

// FromQuartz converts a Quartz cron expression (6 or 7 fields) into the
// equivalent standard 5-field cron string. The returned string is guaranteed to
// be syntactically standard-cron shaped, but callers should still run it through
// parse.Parse to validate ranges/semantics (FromQuartz focuses on the
// dialect-shape translation, not full 5-field validation).
//
// Lossless conversions:
//   - seconds == "0" (fire at the top of the minute) -> dropped.
//   - the `?` marker in day-of-month or day-of-week   -> "*".
//   - year == "*" or omitted                          -> dropped.
//   - Quartz weekday numbers 1-7 (SUN-SAT)            -> cron 0-6 (SUN-SAT).
//
// Refused (returns *ConvertError) when standard cron cannot express it:
//   - a non-zero or wildcard seconds field (sub-minute precision).
//   - a specific year or year range/list.
//   - the `L` (last), `W` (nearest weekday), or `#` (nth weekday) specials.
func FromQuartz(expr string) (string, error) {
	trimmed := strings.TrimSpace(expr)
	if trimmed == "" {
		return "", &ConvertError{Dialect: Quartz, Msg: "empty expression"}
	}

	fields := strings.Fields(trimmed)
	if len(fields) != 6 && len(fields) != 7 {
		return "", &ConvertError{
			Dialect: Quartz,
			Msg: fmt.Sprintf("expected 6 or 7 fields (sec min hour day-of-month month day-of-week [year]), got %d",
				len(fields)),
		}
	}

	seconds := fields[0]
	minute := fields[1]
	hour := fields[2]
	dom := fields[3]
	month := fields[4]
	dow := fields[5]
	year := ""
	if len(fields) == 7 {
		year = fields[6]
	}

	// Seconds: standard cron has no seconds field, so the only value we can carry
	// losslessly is "fire at second 0". Anything else (a specific second, a range,
	// or "*" meaning every second) is sub-minute precision cron cannot express.
	if seconds != "0" {
		return "", &ConvertError{
			Dialect: Quartz,
			Field:   "seconds",
			Msg:     fmt.Sprintf("standard cron has no seconds field; only \"0\" converts losslessly, got %q", seconds),
			Lossy:   true,
		}
	}

	// Year: standard cron has no year field. Only an unrestricted year ("*" or
	// omitted) is representable; a specific year or year set would be dropped and
	// silently change the schedule's meaning, so refuse it.
	if year != "" && year != "*" {
		return "", &ConvertError{
			Dialect: Quartz,
			Field:   "year",
			Msg:     fmt.Sprintf("standard cron has no year field; %q would be lost", year),
			Lossy:   true,
		}
	}

	// Day-of-month / day-of-week: reject Quartz-only special characters up front
	// with a targeted message (they read as gibberish to a standard-cron parser).
	if err := rejectQuartzSpecials(dom, "day-of-month"); err != nil {
		return "", err
	}
	if err := rejectQuartzSpecials(dow, "day-of-week"); err != nil {
		return "", err
	}

	// Quartz demands `?` in exactly one of day-of-month / day-of-week (it forbids
	// specifying both). Standard cron has no `?`; it uses `*` and applies its own
	// DOM-or-DOW rule. Translate `?` -> `*`.
	if dom == "?" {
		dom = "*"
	}
	if dow == "?" {
		dow = "*"
	}

	// Remap Quartz weekday numbering (1=SUN..7=SAT) to standard cron (0=SUN..6=SAT).
	remappedDOW, err := remapQuartzDOW(dow)
	if err != nil {
		return "", err
	}

	// Assemble the standard 5-field expression.
	return strings.Join([]string{minute, hour, dom, month, remappedDOW}, " "), nil
}

// rejectQuartzSpecials returns a *ConvertError if the field text uses a
// Quartz-only special character (`L`, `W`, or `#`) that standard cron cannot
// express. It inspects each comma-separated token so it does not false-positive
// on the letters that legitimately appear inside weekday names — the `W` in
// `WED` and the `L` implied nowhere are not specials, whereas `15W`, `6L`, and
// `6#3` are. Detection rules, applied per token (case-insensitive):
//   - any `#` anywhere            -> `#` "nth weekday of the month".
//   - a token that is exactly `L`
//     or `LW`, or ends in `L`
//     but is not a known day name -> `L` "last".
//   - a `W` immediately preceded
//     by a digit                  -> `W` "nearest weekday".
func rejectQuartzSpecials(text, fieldName string) error {
	for _, token := range strings.Split(text, ",") {
		tok := strings.TrimSpace(token)
		if tok == "" {
			continue
		}
		upper := strings.ToUpper(tok)

		if strings.Contains(upper, "#") {
			return specialErr(fieldName, "#", "\"nth weekday of the month\"")
		}

		// `L` (last): the bare marker `L`, the combo `LW` (last weekday), or a
		// suffix `L` on a value (`6L`, `FRIL`). Guard against matching the `L`
		// that could appear inside a future day alias by requiring it to be a
		// leading/trailing marker rather than an interior letter of a known name.
		if upper == "L" || upper == "LW" || (strings.HasSuffix(upper, "L") && !isKnownDOWName(upper)) {
			return specialErr(fieldName, "L", "\"last\"")
		}

		// `W` (nearest weekday) is always attached to a day-of-month number in
		// Quartz, e.g. `15W`. Only flag a `W` that directly follows a digit so
		// weekday names like WED are left alone.
		if w := strings.IndexByte(upper, 'W'); w > 0 && upper[w-1] >= '0' && upper[w-1] <= '9' {
			return specialErr(fieldName, "W", "\"nearest weekday\"")
		}
	}
	return nil
}

// isKnownDOWName reports whether an upper-cased token is one of the shared
// three-letter weekday abbreviations, so the `L`-suffix check does not misfire
// on a name (none currently end in L, but this keeps the guard explicit).
func isKnownDOWName(upper string) bool {
	_, ok := quartzDOWNames[upper]
	return ok
}

// specialErr builds the standard "no equivalent" ConvertError for a Quartz-only
// special character.
func specialErr(fieldName, found, meaning string) error {
	return &ConvertError{
		Dialect: Quartz,
		Field:   fieldName,
		Msg:     fmt.Sprintf("Quartz %s (%s) has no standard-cron equivalent", found, meaning),
	}
}

// remapQuartzDOW rewrites a Quartz day-of-week field into standard cron
// numbering. Quartz numbers weekdays 1-7 as SUN-SAT; standard cron numbers them
// 0-6 as SUN-SAT. Named days (SUN..SAT) are shared between the dialects and are
// passed through unchanged. The field may contain "*", lists (","), ranges
// ("-"), and steps ("/"); every numeric token is shifted down by one, and
// ranges are normalized so the parser never sees an inverted or out-of-range
// value that only *looks* wrong because of the numbering shift.
func remapQuartzDOW(text string) (string, error) {
	if text == "*" {
		return "*", nil
	}

	terms := strings.Split(text, ",")
	out := make([]string, 0, len(terms))
	for _, term := range terms {
		if term == "" {
			return "", &ConvertError{Dialect: Quartz, Field: "day-of-week", Msg: "empty term in comma list"}
		}

		// Split off an optional "/step" — the step is a count, not a weekday, so
		// it is carried through unchanged.
		body := term
		step := ""
		if slash := strings.IndexByte(term, '/'); slash >= 0 {
			body = term[:slash]
			step = term[slash+1:]
		}

		var remappedBody string
		if dash := strings.IndexByte(body, '-'); dash >= 0 {
			lo, err := remapQuartzDOWToken(body[:dash])
			if err != nil {
				return "", err
			}
			hi, err := remapQuartzDOWToken(body[dash+1:])
			if err != nil {
				return "", err
			}
			remappedBody = fmt.Sprintf("%s-%s", lo, hi)
		} else {
			tok, err := remapQuartzDOWToken(body)
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

// remapQuartzDOWToken remaps a single day-of-week token (a bare "*", a named day,
// or a number) from Quartz to standard cron numbering. Named days are returned
// unchanged; numbers 1-7 map to 0-6. Quartz also tolerates 0 as an alias for
// Sunday in some deployments, so 0 is accepted and left as 0 (Sunday) rather
// than rejected.
func remapQuartzDOWToken(tok string) (string, error) {
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return "", &ConvertError{Dialect: Quartz, Field: "day-of-week", Msg: "empty value"}
	}
	if tok == "*" {
		return "*", nil
	}

	// Named weekdays are shared between dialects; keep them as-is (upper-cased for
	// consistency with the standard parser's expectations).
	upper := strings.ToUpper(tok)
	if _, ok := quartzDOWNames[upper]; ok {
		return upper, nil
	}

	n, err := strconv.Atoi(tok)
	if err != nil {
		return "", &ConvertError{
			Dialect: Quartz,
			Field:   "day-of-week",
			Msg:     fmt.Sprintf("%q is not a valid Quartz weekday (expected 1-7 or SUN-SAT)", tok),
		}
	}
	// Quartz weekdays are 1-7 (SUN-SAT). Accept 0 as a lenient Sunday alias.
	if n == 0 {
		return "0", nil
	}
	if n < 1 || n > 7 {
		return "", &ConvertError{
			Dialect: Quartz,
			Field:   "day-of-week",
			Msg:     fmt.Sprintf("%d is out of range (Quartz weekdays are 1-7, SUN-SAT)", n),
		}
	}
	// Shift 1-7 (SUN-SAT) down to 0-6 (SUN-SAT).
	return strconv.Itoa(n - 1), nil
}
