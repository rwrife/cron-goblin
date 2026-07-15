// Package narrate turns a cron schedule (or a schedule *change*) into a warm,
// prose one-liner suitable for changelogs, release notes, and PR descriptions.
//
// It is the human-readable counterpart to internal/explain: where `explain`
// produces a terse, structured "when it fires" clause, `narrate` wraps that
// grammar into a full sentence you could paste into a CHANGELOG entry — and,
// given a from/to pair, describes the *change* (how the firing cadence shifts
// and when the next run moves to).
//
// Like explain, this package is pure, deterministic, and offline: same input,
// same sentence. No LLM, no network. The cadence classification is derived
// entirely from the normalized schedule fields via the shared grammar.
package narrate

import (
	"fmt"
	"strings"
	"time"

	"github.com/rwrife/cron-goblin/internal/explain"
	"github.com/rwrife/cron-goblin/internal/nextrun"
	"github.com/rwrife/cron-goblin/internal/parse"
)

// Narrate returns a single prose sentence describing when the schedule fires,
// phrased for a changelog ("This job runs …"). The sentence is capitalized and
// ends with a period.
func Narrate(s parse.Schedule) string {
	clause := lowerFirst(explain.Explain(s))
	return "This job runs " + clause + "."
}

// NarrateChange returns a single prose sentence describing the move from the
// old schedule to the new one — the cadence delta and, when computable, the
// shift in the next fire time. The sentence is capitalized and ends with a
// period.
//
// from is the reference instant used to compute the "next run" shift; loc is
// the timezone those runs are evaluated in (nil defaults to UTC).
func NarrateChange(oldS, newS parse.Schedule, from time.Time, loc *time.Location) string {
	newClause := lowerFirst(explain.Explain(newS))
	oldClause := lowerFirst(explain.Explain(oldS))

	// Identical schedules: nothing to narrate.
	if oldS.Raw == newS.Raw || (oldClause == newClause) {
		return "This job's schedule is unchanged; it still runs " + newClause + "."
	}

	var b strings.Builder
	b.WriteString("This job now runs ")
	b.WriteString(newClause)
	b.WriteString(" instead of ")
	b.WriteString(oldClause)

	if delta := cadenceDelta(oldS, newS, from, loc); delta != "" {
		b.WriteString(" — ")
		b.WriteString(delta)
	}

	if shift := nextRunShift(oldS, newS, from, loc); shift != "" {
		b.WriteString("; ")
		b.WriteString(shift)
	}

	b.WriteString(".")
	return capitalize(b.String())
}

// cadenceDelta describes whether the new schedule fires more or less often than
// the old one, using the count of runs inside a fixed look-ahead window as a
// frequency proxy. Returns "" when the two fire the same number of times (so we
// don't assert a delta that isn't there) or when neither ever fires.
func cadenceDelta(oldS, newS parse.Schedule, from time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	const window = 30 * 24 * time.Hour
	oldN := countInWindow(oldS, from, window, loc)
	newN := countInWindow(newS, from, window, loc)

	switch {
	case oldN == 0 && newN == 0:
		return ""
	case oldN == 0 && newN > 0:
		return "it now fires where it previously never did"
	case newN == 0 && oldN > 0:
		return "it now never fires where it previously did"
	case newN == oldN:
		return ""
	case newN > oldN:
		return fmt.Sprintf("about %s more often", ratioPhrase(oldN, newN))
	default:
		return fmt.Sprintf("about %s less often", ratioPhrase(newN, oldN))
	}
}

// countInWindow returns how many times s fires within [from, from+window).
func countInWindow(s parse.Schedule, from time.Time, window time.Duration, loc *time.Location) int {
	end := from.Add(window)
	cur := from
	n := 0
	// Cap iterations so a pathological every-minute schedule can't run away.
	const maxRuns = 100000
	for n < maxRuns {
		next, err := nextrun.Next(s, cur, loc)
		if err != nil || !next.Before(end) {
			break
		}
		n++
		cur = next.Add(time.Minute)
	}
	return n
}

// ratioPhrase renders how many times bigger hi is than lo as a friendly phrase
// ("twice", "3×", "roughly 1.5×"). lo is assumed > 0 and hi >= lo.
func ratioPhrase(lo, hi int) string {
	if lo == 0 {
		return "much"
	}
	r := float64(hi) / float64(lo)
	switch {
	case r >= 1.9 && r <= 2.1:
		return "twice"
	case r == float64(int(r)):
		return fmt.Sprintf("%d×", int(r))
	default:
		return fmt.Sprintf("%.1f×", r)
	}
}

// nextRunShift describes how the very next fire time moves between the two
// schedules. Returns "" when either never fires or the next run is unchanged.
func nextRunShift(oldS, newS parse.Schedule, from time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	oldNext, oerr := nextrun.Next(oldS, from, loc)
	newNext, nerr := nextrun.Next(newS, from, loc)
	if oerr != nil || nerr != nil {
		return ""
	}
	if oldNext.Equal(newNext) {
		return ""
	}
	return fmt.Sprintf("the next run moves from %s to %s",
		oldNext.Format("Mon Jan 2 15:04 MST"), newNext.Format("Mon Jan 2 15:04 MST"))
}

// --- small utilities --------------------------------------------------------

// lowerFirst lower-cases the first rune of s. explain.Explain capitalizes its
// output; inside a prose sentence we want it mid-sentence lowercase.
func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// capitalize upper-cases the first rune of s.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
