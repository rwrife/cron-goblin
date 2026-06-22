// Package parse turns a standard 5-field cron string into a normalized,
// validated Schedule. It is the trusted core of cron-goblin: every other
// logic package (explain, nextrun, lint) consumes the Schedule produced here
// rather than re-parsing raw cron text.
//
// Supported syntax (standard 5-field cron):
//
//	┌───────────── minute        (0-59)
//	│ ┌─────────── hour          (0-23)
//	│ │ ┌───────── day-of-month  (1-31)
//	│ │ │ ┌─────── month         (1-12 or JAN-DEC)
//	│ │ │ │ ┌───── day-of-week   (0-6 or SUN-SAT; 7 also accepted as Sunday)
//	│ │ │ │ │
//	* * * * *
//
// Within each field we accept:
//   - "*"        every value in range
//   - "a"        a single value (or named month/day, e.g. JAN, MON)
//   - "a-b"      an inclusive range
//   - "a-b/n"    a stepped range
//   - "*/n"      every n values across the whole range
//   - "a/n"      every n values starting at a (through the field max)
//   - "x,y,z"    a comma list of any of the above
//
// We deliberately reject six-field/seconds cron and other dialects in v0.1;
// those are backlog (see PLAN.md).
package parse

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Field identifies one of the five cron positions. It is used for friendly
// error messages and to drive the per-field bounds table.
type Field int

const (
	Minute Field = iota
	Hour
	DOM // day-of-month
	Month
	DOW // day-of-week
)

// fieldName is the human label for a Field, used in error messages.
var fieldName = map[Field]string{
	Minute: "minute",
	Hour:   "hour",
	DOM:    "day-of-month",
	Month:  "month",
	DOW:    "day-of-week",
}

func (f Field) String() string { return fieldName[f] }

// bounds describes the legal numeric range for a field.
type bounds struct {
	min, max int
}

// fieldBounds is the legal range for each field. Day-of-week is 0-6 here;
// the special value 7 (also Sunday) is normalized to 0 during parsing.
var fieldBounds = map[Field]bounds{
	Minute: {0, 59},
	Hour:   {0, 23},
	DOM:    {1, 31},
	Month:  {1, 12},
	DOW:    {0, 6},
}

// monthNames maps three-letter month abbreviations to their cron number.
var monthNames = map[string]int{
	"JAN": 1, "FEB": 2, "MAR": 3, "APR": 4, "MAY": 5, "JUN": 6,
	"JUL": 7, "AUG": 8, "SEP": 9, "OCT": 10, "NOV": 11, "DEC": 12,
}

// dowNames maps three-letter weekday abbreviations to their cron number
// (Sunday = 0). SUN may also appear as 7 numerically; see normalizeDOW.
var dowNames = map[string]int{
	"SUN": 0, "MON": 1, "TUE": 2, "WED": 3, "THU": 4, "FRI": 5, "SAT": 6,
}

// FieldSpec is the parsed, normalized form of a single cron field. Values is
// the sorted, de-duplicated set of concrete numbers the field matches; Star
// records whether the field was written as a bare "*" (semantically "every
// value"), which explain and lint use to phrase things naturally and to apply
// cron's day-of-month/day-of-week OR rule.
type FieldSpec struct {
	Field  Field
	Values []int
	Star   bool
	// Raw is the original text of this field, preserved for diagnostics.
	Raw string
}

// Schedule is the normalized representation of a 5-field cron expression.
// It is intentionally simple and dependency-free so every downstream package
// can rely on it.
type Schedule struct {
	Minute FieldSpec
	Hour   FieldSpec
	DOM    FieldSpec
	Month  FieldSpec
	DOW    FieldSpec
	// Raw is the original (whitespace-normalized) expression.
	Raw string
}

// Fields returns the five field specs in canonical cron order. Handy for
// generic iteration in explain/lint and in tests.
func (s Schedule) Fields() []FieldSpec {
	return []FieldSpec{s.Minute, s.Hour, s.DOM, s.Month, s.DOW}
}

// Error is a parse failure with enough context to point the user at the
// offending field. It implements error and is comparable in tests via the
// Field/Expr fields.
type Error struct {
	Expr  string // the full expression that failed
	Field Field  // which field (when applicable)
	Msg   string // human explanation
}

func (e *Error) Error() string {
	return fmt.Sprintf("cron %s field %q: %s", e.Field, e.fieldText(), e.Msg)
}

// fieldText returns the raw text of the failing field, best-effort.
func (e *Error) fieldText() string {
	parts := strings.Fields(e.Expr)
	if int(e.Field) < len(parts) {
		return parts[e.Field]
	}
	return ""
}

// Parse parses a standard 5-field cron expression into a Schedule. Leading and
// trailing whitespace is ignored and fields may be separated by any run of
// spaces or tabs. On failure it returns an *Error describing the problem.
func Parse(expr string) (Schedule, error) {
	trimmed := strings.TrimSpace(expr)
	if trimmed == "" {
		return Schedule{}, &Error{Expr: expr, Field: Minute, Msg: "empty expression"}
	}

	parts := strings.Fields(trimmed)
	if len(parts) != 5 {
		// One of the most common mistakes: pasting a 6-field (seconds) cron.
		hint := ""
		if len(parts) == 6 {
			hint = " (this looks like 6-field/seconds cron, which v0.1 does not support yet)"
		}
		return Schedule{}, &Error{
			Expr:  trimmed,
			Field: Minute,
			Msg: fmt.Sprintf("expected 5 fields, got %d%s — format is `min hour day-of-month month day-of-week`",
				len(parts), hint),
		}
	}

	order := []Field{Minute, Hour, DOM, Month, DOW}
	specs := make([]FieldSpec, len(order))
	for i, f := range order {
		spec, err := parseField(trimmed, f, parts[i])
		if err != nil {
			return Schedule{}, err
		}
		specs[i] = spec
	}

	return Schedule{
		Minute: specs[0],
		Hour:   specs[1],
		DOM:    specs[2],
		Month:  specs[3],
		DOW:    specs[4],
		Raw:    strings.Join(parts, " "),
	}, nil
}

// parseField parses a single field's text into a FieldSpec. It splits on
// commas and unions the resulting value sets.
func parseField(expr string, f Field, text string) (FieldSpec, error) {
	if text == "" {
		return FieldSpec{}, &Error{Expr: expr, Field: f, Msg: "empty field"}
	}

	b := fieldBounds[f]
	set := map[int]struct{}{}
	star := false

	for _, term := range strings.Split(text, ",") {
		if term == "" {
			return FieldSpec{}, &Error{Expr: expr, Field: f, Msg: "empty term in comma list"}
		}
		vals, isStar, err := parseTerm(expr, f, b, term)
		if err != nil {
			return FieldSpec{}, err
		}
		if isStar {
			star = true
		}
		for _, v := range vals {
			set[v] = struct{}{}
		}
	}

	values := make([]int, 0, len(set))
	for v := range set {
		values = append(values, v)
	}
	sort.Ints(values)

	// A field is only "*" semantically if it was written as a bare star with no
	// other comma terms. "*/1" covers the full range too but reads differently,
	// so we treat Star strictly as "was literally *".
	if star && len(strings.Split(text, ",")) > 1 {
		star = false
	}

	return FieldSpec{Field: f, Values: values, Star: star, Raw: text}, nil
}

// parseTerm parses one comma-free term: "*", "a", "a-b", or any of those with
// a "/n" step suffix. It returns the concrete matched values and whether the
// term was a bare "*".
func parseTerm(expr string, f Field, b bounds, term string) (values []int, star bool, err error) {
	rangePart := term
	step := 1
	hasStep := false

	// Split off an optional "/step" suffix.
	if slash := strings.IndexByte(term, '/'); slash >= 0 {
		hasStep = true
		rangePart = term[:slash]
		stepStr := term[slash+1:]
		if stepStr == "" {
			return nil, false, &Error{Expr: expr, Field: f, Msg: fmt.Sprintf("missing step after '/' in %q", term)}
		}
		step, err = strconv.Atoi(stepStr)
		if err != nil {
			return nil, false, &Error{Expr: expr, Field: f, Msg: fmt.Sprintf("step %q is not a number in %q", stepStr, term)}
		}
		if step <= 0 {
			return nil, false, &Error{Expr: expr, Field: f, Msg: fmt.Sprintf("step must be positive in %q", term)}
		}
	}

	var lo, hi int
	isStar := false

	switch {
	case rangePart == "*":
		lo, hi = b.min, b.max
		isStar = true
	case strings.ContainsRune(rangePart, '-'):
		// "a-b" inclusive range.
		dash := strings.IndexByte(rangePart, '-')
		loVal, e1 := parseValue(expr, f, rangePart[:dash])
		hiVal, e2 := parseValue(expr, f, rangePart[dash+1:])
		if e1 != nil {
			return nil, false, e1
		}
		if e2 != nil {
			return nil, false, e2
		}
		lo, hi = loVal, hiVal
		if lo > hi {
			return nil, false, &Error{Expr: expr, Field: f, Msg: fmt.Sprintf("range %q is inverted (%d > %d)", rangePart, lo, hi)}
		}
	default:
		// A single value. With a step ("a/n"), it means "from a to the field
		// max, every n" — matching common cron implementations.
		v, e := parseValue(expr, f, rangePart)
		if e != nil {
			return nil, false, e
		}
		if hasStep {
			lo, hi = v, b.max
		} else {
			lo, hi = v, v
		}
	}

	// Materialize the (possibly stepped) range into concrete values.
	for v := lo; v <= hi; v += step {
		values = append(values, v)
	}
	// A bare "*" with no step is the only true "every value" form.
	return values, isStar && !hasStep, nil
}

// parseValue parses a single numeric-or-named value and bounds-checks it.
func parseValue(expr string, f Field, s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, &Error{Expr: expr, Field: f, Msg: "empty value"}
	}

	// Named months / weekdays are case-insensitive.
	upper := strings.ToUpper(s)
	switch f {
	case Month:
		if n, ok := monthNames[upper]; ok {
			return n, nil
		}
	case DOW:
		if n, ok := dowNames[upper]; ok {
			return n, nil
		}
	}

	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, &Error{Expr: expr, Field: f, Msg: fmt.Sprintf("%q is not a valid value", s)}
	}

	// Accept 7 as Sunday in the day-of-week field, normalizing to 0.
	if f == DOW && n == 7 {
		n = 0
	}

	b := fieldBounds[f]
	if n < b.min || n > b.max {
		return 0, &Error{Expr: expr, Field: f, Msg: fmt.Sprintf("%d is out of range (%d-%d)", n, b.min, b.max)}
	}
	return n, nil
}
