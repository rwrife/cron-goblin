// rule_frequent.go implements the too-frequent rule: jobs that fire very often
// (every minute, or close to it) are usually a mistake — a runaway loop, a
// forgotten debug schedule, or a job that should be a long-running service
// instead of a cron entry. We warn rather than error: such schedules are valid
// cron, just suspicious.
package lint

import (
	"fmt"
	"time"

	"github.com/rwrife/cron-goblin/internal/nextrun"
)

// defaultMinInterval is the smallest gap (in minutes) between consecutive fire
// times we consider sane by default. A schedule whose tightest interval is at
// or below this is flagged. Two minutes catches every-minute (`* * * * *`) and
// the common `*/1`/`*/2` debug leftovers without nagging about `*/5`.
const defaultMinInterval = 2

// tooFrequentRule warns about schedules that fire too often. Rather than
// pattern-match on the raw text (which misses things like `0-59 * * * *`), it
// measures the real cadence by sampling consecutive fire times from the engine
// and taking the minimum gap. That way the rule agrees with what the schedule
// actually does.
type tooFrequentRule struct {
	// minMinutes is the threshold: a tightest-gap <= this many minutes warns.
	minMinutes int
}

// Name returns the stable rule code.
func (tooFrequentRule) Name() string { return "too-frequent" }

// sampleRuns is how many consecutive fire times we examine to estimate the
// tightest interval. A handful is plenty to catch sub-threshold cadences while
// staying cheap.
const sampleRuns = 6

// Check warns for each parseable entry whose minimum gap between consecutive
// fires is at or below the configured threshold. Schedules that never fire, or
// fire fewer than twice within the horizon, can't be "too frequent" and are
// skipped.
func (r tooFrequentRule) Check(entries []Entry) []Finding {
	threshold := r.minMinutes
	if threshold <= 0 {
		threshold = defaultMinInterval
	}
	from := time.Now()

	var out []Finding
	for _, e := range entries {
		if e.ParseErr != nil {
			continue
		}
		runs := nextrun.NextN(e.Schedule, from, sampleRuns, time.UTC)
		if len(runs) < 2 {
			continue
		}
		minGap := runs[1].Sub(runs[0])
		for i := 2; i < len(runs); i++ {
			if gap := runs[i].Sub(runs[i-1]); gap < minGap {
				minGap = gap
			}
		}

		if minGap <= time.Duration(threshold)*time.Minute {
			out = append(out, Finding{
				Rule:     "too-frequent",
				Severity: SeverityWarning,
				Message: fmt.Sprintf(
					"`%s` fires every %s — that's a very tight cadence; double-check this isn't a runaway or a leftover debug schedule",
					e.Schedule.Raw, humanizeGap(minGap)),
				Lines: []int{e.Line},
			})
		}
	}
	return out
}

// humanizeGap renders a fire-to-fire gap in friendly units. Cron resolution is
// whole minutes, so we report minutes (and hours when it divides cleanly).
func humanizeGap(d time.Duration) string {
	mins := int(d.Round(time.Minute) / time.Minute)
	switch {
	case mins <= 1:
		return "minute"
	case mins < 60:
		return fmt.Sprintf("%d minutes", mins)
	case mins%60 == 0:
		h := mins / 60
		if h == 1 {
			return "hour"
		}
		return fmt.Sprintf("%d hours", h)
	default:
		return fmt.Sprintf("%d minutes", mins)
	}
}
