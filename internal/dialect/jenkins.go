package dialect

import (
	"crypto/md5" // Jenkins' compatibility hash, not a security primitive.
	"encoding/binary"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/rwrife/cron-goblin/internal/parse"
)

var jenkinsHashTerm = regexp.MustCompile(`^H(?:\(([0-9]+)-([0-9]+)\))?(?:/([0-9]+))?$`)
var jenkinsPlainTerm = regexp.MustCompile(`^(?:[0-9]+(?:-[0-9]+)?|\*)(?:/[0-9]+)?$`)

// FromJenkins resolves H using Jenkins' MD5-seeded java.util.Random sequence.
// The seed is the full job name, including any folder path.
func FromJenkins(expr, jobName string) (string, error) {
	macros := map[string]string{
		"@yearly": "H H H H *", "@annually": "H H H H *",
		"@monthly": "H H H * *", "@weekly": "H H * * H",
		"@daily": "H H * * *", "@midnight": "H H(0-2) * * *",
		"@hourly": "H * * * *",
	}
	expr = strings.TrimSpace(expr)
	if expanded, ok := macros[expr]; ok {
		expr = expanded
	}
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return "", &ConvertError{Dialect: Jenkins, Msg: "expected 5 fields (min hour day-of-month month day-of-week) or a Jenkins @macro"}
	}
	if strings.Contains(expr, "H") && strings.TrimSpace(jobName) == "" {
		return "", &ConvertError{Dialect: Jenkins, Msg: "H needs --job-name (the full Jenkins job name) to resolve deterministically"}
	}
	random := newJenkinsRandom(jobName)
	mins := []int{0, 0, 1, 1, 0}
	maxs := []int{59, 23, 31, 12, 7}
	for i, field := range fields {
		terms := strings.Split(field, ",")
		for j, term := range terms {
			if !strings.Contains(term, "H") {
				// Jenkins does not accept named months/weekdays or a/n steps.
				if !jenkinsPlainTerm.MatchString(term) || (strings.Contains(term, "/") && !strings.ContainsAny(strings.SplitN(term, "/", 2)[0], "*-")) {
					return "", &ConvertError{Dialect: Jenkins, Field: parse.Field(i).String(), Msg: fmt.Sprintf("unsupported Jenkins term %q; use numeric values/ranges, */n, or H", term)}
				}
				if i == 4 {
					normalized, err := jenkinsWeekdays(term)
					if err != nil {
						return "", err
					}
					terms[j] = normalized
				}
				continue
			}
			match := jenkinsHashTerm.FindStringSubmatch(term)
			if match == nil {
				return "", &ConvertError{Dialect: Jenkins, Field: parse.Field(i).String(), Msg: fmt.Sprintf("invalid H term %q; expected H, H/n, H(a-b), or H(a-b)/n", term)}
			}
			lo, hi := mins[i], maxs[i]
			if i == 2 {
				hi = 28
			} else if i == 4 {
				hi = 6
			}
			if match[1] != "" {
				var err error
				lo, err = strconv.Atoi(match[1])
				if err != nil {
					return "", &ConvertError{Dialect: Jenkins, Field: parse.Field(i).String(), Msg: "H range is too large"}
				}
				hi, err = strconv.Atoi(match[2])
				if err != nil {
					return "", &ConvertError{Dialect: Jenkins, Field: parse.Field(i).String(), Msg: "H range is too large"}
				}
			}
			if lo < mins[i] || hi > maxs[i] || lo > hi {
				return "", &ConvertError{Dialect: Jenkins, Field: parse.Field(i).String(), Msg: fmt.Sprintf("invalid H range %d-%d (allowed %d-%d)", lo, hi, mins[i], maxs[i])}
			}
			step := 1
			if match[3] != "" {
				var err error
				step, err = strconv.Atoi(match[3])
				if err != nil || step < 1 || step > hi-lo+1 {
					return "", &ConvertError{Dialect: Jenkins, Field: parse.Field(i).String(), Msg: fmt.Sprintf("H step must be between 1 and %d", hi-lo+1)}
				}
			}
			values := []string{}
			if step == 1 {
				values = append(values, strconv.Itoa(lo+random.next(hi-lo+1)))
			} else {
				for value := lo + random.next(step); value <= hi; value += step {
					values = append(values, strconv.Itoa(value))
				}
			}
			terms[j] = strings.Join(values, ",")
		}
		fields[i] = strings.Join(terms, ",")
	}
	sched, err := parse.Parse(strings.Join(fields, " "))
	if err != nil {
		return "", &ConvertError{Dialect: Jenkins, Msg: err.Error()}
	}
	if len(sched.DOM.Values) == 31 {
		fields[2] = "*"
	}
	if len(sched.DOW.Values) == 7 {
		fields[4] = "*"
	}
	if fields[2] != "*" && fields[4] != "*" {
		return "", &ConvertError{Dialect: Jenkins, Field: "day-of-month/day-of-week", Msg: "Jenkins ANDs the day fields; standard cron ORs them, so two restricted day fields cannot convert losslessly", Lossy: true}
	}
	return strings.Join(fields, " "), nil
}

func jenkinsWeekdays(term string) (string, error) {
	if term == "*" {
		return term, nil
	}
	invalid := func() (string, error) {
		return "", &ConvertError{Dialect: Jenkins, Field: "day-of-week", Msg: fmt.Sprintf("invalid weekday term %q; expected 0-7 with a positive step", term)}
	}
	parts := strings.SplitN(term, "/", 2)
	step := 1
	if len(parts) == 2 {
		var err error
		step, err = strconv.Atoi(parts[1])
		if err != nil || step < 1 {
			return invalid()
		}
	}
	lo, hi := 0, 7
	if parts[0] != "*" {
		bounds := strings.SplitN(parts[0], "-", 2)
		var err error
		lo, err = strconv.Atoi(bounds[0])
		if err != nil {
			return invalid()
		}
		hi = lo
		if len(bounds) == 2 {
			hi, err = strconv.Atoi(bounds[1])
			if err != nil {
				return invalid()
			}
		}
	}
	if lo < 0 || hi > 7 || lo > hi {
		return invalid()
	}
	values := []string{}
	for n := lo; n <= hi; {
		values = append(values, strconv.Itoa(n%7))
		if step > hi-n {
			break
		}
		n += step
	}
	return strings.Join(values, ","), nil
}

// ToJenkins intentionally jitters only the minute field. A fixed minute becomes
// H; uniform whole-hour intervals become H/n. Other fields retain their meaning.
func ToJenkins(expr string) (string, error) {
	sched, err := parse.Parse(expr)
	if err != nil {
		return "", &ConvertError{Dialect: Jenkins, Msg: err.Error()}
	}
	fields := strings.Fields(sched.Raw)
	if !sched.DOM.Star && !sched.DOW.Star {
		if len(sched.DOM.Values) == 31 || len(sched.DOW.Values) == 7 {
			// Cron's OR is always true if either explicit day set is complete.
			fields[2], fields[4] = "*", "*"
		} else {
			return "", &ConvertError{Dialect: Jenkins, Field: "day-of-month/day-of-week", Msg: "cron ORs the day fields; Jenkins ANDs them, so two restricted day fields cannot convert losslessly", Lossy: true}
		}
	}
	switch {
	case len(sched.Minute.Values) == 1:
		fields[0] = "H"
	case len(sched.Minute.Values) == 60:
		fields[0] = "*"
	default:
		values := sched.Minute.Values
		step := values[1] - values[0]
		uniform := step > 1 && 60%step == 0 && len(values) == 60/step && values[0] < step
		for i := 1; i < len(values); i++ {
			uniform = uniform && values[i]-values[i-1] == step
		}
		if !uniform {
			return "", &ConvertError{Dialect: Jenkins, Field: "minute", Msg: "cannot jitter this pattern without changing its cadence; use a fixed minute or an evenly spaced whole-hour interval", Lossy: true}
		}
		fields[0] = fmt.Sprintf("H/%d", step)
	}
	for i, spec := range sched.Fields()[1:] {
		if (i == 1 || i == 3) && fields[i+1] == "*" {
			continue
		}
		if spec.Star {
			fields[i+1] = "*"
			continue
		}
		values := make([]string, len(spec.Values))
		for j, value := range spec.Values {
			values[j] = strconv.Itoa(value)
		}
		fields[i+1] = strings.Join(values, ",")
	}
	return strings.Join(fields, " "), nil
}

// Jenkins folds MD5 into a 64-bit seed, then uses Java's 48-bit LCG and bounded
// rejection sampling. Go's math/rand has a different sequence and cannot substitute.
type jenkinsRandom struct{ state uint64 }

func newJenkinsRandom(jobName string) *jenkinsRandom {
	digest := md5.Sum([]byte(jobName))
	for i := 0; i < 8; i++ {
		digest[i] ^= digest[i+8]
	}
	return &jenkinsRandom{state: (binary.BigEndian.Uint64(digest[:8]) ^ 0x5deece66d) & ((1 << 48) - 1)}
}

func (r *jenkinsRandom) next(bound int) int {
	for {
		r.state = (r.state*0x5deece66d + 11) & ((1 << 48) - 1)
		bits := int(r.state >> 17)
		if bound&(bound-1) == 0 {
			return int((int64(bound) * int64(bits)) >> 31)
		}
		value := bits % bound
		if int64(bits)-int64(value)+int64(bound)-1 < 1<<31 {
			return value
		}
	}
}
