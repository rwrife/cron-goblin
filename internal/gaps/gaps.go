// Package gaps computes the quiet windows in a crontab: the longest stretches
// of time over a look-ahead window where *nothing* is scheduled to fire. It is
// the inverse of the thundering-herd detector — instead of "too much at once",
// it answers "when is this box actually quiet?", the natural place to slot a
// heavy backup, a deploy freeze, or a maintenance reboot.
//
// Like nextrun, this package is pure and deterministic: same crontab, same
// start instant, same timezone in; same gaps out. The goblin's personality
// lives in internal/goblin, not here.
//
// # Model
//
// Cron fires are minute-aligned. A job that fires at minute M is treated as
// occupying the half-open minute [M, M+1min): the box is "busy" for that
// minute. A gap is a maximal stretch of time within the window during which no
// job occupies any minute. Gap boundaries are clamped to the window edges, so
// a crontab with no fires at all yields a single gap spanning the whole window.
//
// A job that fires every minute leaves no quiet minute and therefore produces
// zero gaps. Dead expressions (e.g. Feb 30th) never fire and are simply absent
// from the fire set — they neither create nor fill gaps.
package gaps

import (
	"sort"
	"strings"
	"time"

	"github.com/rwrife/cron-goblin/internal/lint"
	"github.com/rwrife/cron-goblin/internal/nextrun"
	"github.com/rwrife/cron-goblin/internal/parse"
)

// DefaultDays is the default look-ahead window, in days, when the caller does
// not specify one. Seven days covers a full weekly cron cycle, which is the
// period most schedules repeat over.
const DefaultDays = 7

// DefaultTop caps how many gaps are reported by default, longest first.
const DefaultTop = 5

// Gap is one quiet interval: [Start, End) during which nothing fires. Duration
// is End.Sub(Start). Boundaries are clamped to the analysis window.
type Gap struct {
	Start    time.Time
	End      time.Time
	Duration time.Duration
}

// Busiest records the single minute within the window on which the most jobs
// fire, with that job count. Time is the minute-aligned instant; Count is how
// many fires land on it. When nothing fires at all, Count is 0 and Time is the
// window start.
type Busiest struct {
	Time  time.Time
	Count int
}

// Report is the result of analyzing a crontab for quiet windows. Gaps are
// sorted longest first (ties broken by earlier Start). Skipped counts source
// lines that could not be used for gap math (parse errors or never-fires),
// which the caller may surface as a grumpy note.
type Report struct {
	Window   time.Duration
	From     time.Time
	To       time.Time
	Gaps     []Gap
	Busiest  Busiest
	Skipped  int
	Analyzed int // schedule-bearing lines that contributed at least considered
}

// Analyze reads a crontab from src, merges the fire times of every valid job
// over the window [from, from+days*24h] in loc, and reports the top idle
// intervals (longest first) plus the busiest minute. loc nil means UTC. days
// <= 0 falls back to DefaultDays; top <= 0 returns all gaps.
//
// Lines that fail to parse, and schedules that never fire within the window,
// are ignored for gap math and counted in Report.Skipped.
func Analyze(src string, from time.Time, days, top int, loc *time.Location) (Report, error) {
	if loc == nil {
		loc = time.UTC
	}
	if days <= 0 {
		days = DefaultDays
	}
	from = from.In(loc).Truncate(time.Minute)
	window := time.Duration(days) * 24 * time.Hour
	to := from.Add(window)

	entries, err := lint.ParseCrontab(strings.NewReader(src))
	if err != nil {
		return Report{}, err
	}

	rep := Report{Window: window, From: from, To: to}

	// fireCount maps a minute-aligned unix instant to how many jobs fire on it.
	fireCount := map[int64]int{}
	for _, e := range entries {
		if e.ParseErr != nil {
			rep.Skipped++
			continue
		}
		times := runsInWindow(e.Schedule, from, to, loc)
		if len(times) == 0 {
			// Never fires within the window (dead or simply sparse past the
			// edge). It contributes no busy minutes.
			rep.Skipped++
			continue
		}
		rep.Analyzed++
		for _, t := range times {
			fireCount[t.Unix()]++
		}
	}

	rep.Busiest = busiestMinute(fireCount, from, loc)
	rep.Gaps = computeGaps(fireCount, from, to, top, loc)
	return rep, nil
}

// runsInWindow returns every fire time in [from, to], minute-aligned, in loc.
// The window is inclusive of `from` itself: if the schedule fires exactly on
// the window's opening minute, that minute counts as busy. This is what makes
// an every-minute job correctly yield zero gaps rather than a spurious
// one-minute quiet slot before the first strictly-after fire.
//
// It walks the nextrun engine one fire at a time so both sparse and dense
// schedules terminate at the window edge. A hard cap guards against pathological
// combinations (an every-minute job over a huge window); the deadline check
// normally ends the loop first.
func runsInWindow(s parse.Schedule, from, to time.Time, loc *time.Location) []time.Time {
	out := []time.Time{}
	// nextrun.Next fires strictly after its argument, so to include a fire at
	// `from` itself we seed the walk one minute earlier.
	cur := from.Add(-time.Minute)
	const hardCap = 2_000_000
	for i := 0; i < hardCap; i++ {
		t, err := nextrun.Next(s, cur, loc)
		if err != nil {
			break
		}
		if t.After(to) {
			break
		}
		if t.Before(from) {
			cur = t
			continue
		}
		out = append(out, t)
		cur = t
	}
	return out
}

// busiestMinute finds the minute with the most concurrent fires. Ties are
// broken deterministically by the earliest instant. When nothing fires, Count
// is 0 and Time is the window start.
func busiestMinute(fireCount map[int64]int, from time.Time, loc *time.Location) Busiest {
	best := Busiest{Time: from, Count: 0}
	// Iterate in sorted key order so ties resolve to the earliest minute
	// regardless of Go's random map iteration.
	keys := make([]int64, 0, len(fireCount))
	for k := range fireCount {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	for _, k := range keys {
		if fireCount[k] > best.Count {
			best.Count = fireCount[k]
			best.Time = time.Unix(k, 0).In(loc)
		}
	}
	return best
}

// computeGaps derives the quiet intervals from the set of busy minutes. Each
// fired minute M occupies [M, M+1min); a gap is a maximal stretch between the
// end of one busy minute and the start of the next, clamped to [from, to].
// Results are sorted longest first, ties broken by earlier Start, then capped
// to top (top <= 0 returns all).
func computeGaps(fireCount map[int64]int, from, to time.Time, top int, loc *time.Location) []Gap {
	// Sorted, de-duplicated busy minute starts within the window.
	starts := make([]time.Time, 0, len(fireCount))
	for k := range fireCount {
		starts = append(starts, time.Unix(k, 0).In(loc))
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i].Before(starts[j]) })

	gaps := []Gap{}
	// cursor tracks the end of the most recent busy span (start clamped to the
	// window). It begins at the window start: any quiet time before the first
	// fire is a leading gap.
	cursor := from
	for _, ms := range starts {
		if !ms.After(cursor) {
			// This busy minute overlaps or abuts the current busy run; advance
			// the cursor to cover its minute.
			end := ms.Add(time.Minute)
			if end.After(cursor) {
				cursor = end
			}
			continue
		}
		// There is quiet time between cursor and this fire's start.
		gaps = appendGap(gaps, cursor, ms)
		cursor = ms.Add(time.Minute)
	}
	// Trailing quiet time from the last busy minute to the window edge.
	if cursor.Before(to) {
		gaps = appendGap(gaps, cursor, to)
	}

	sort.SliceStable(gaps, func(i, j int) bool {
		if gaps[i].Duration != gaps[j].Duration {
			return gaps[i].Duration > gaps[j].Duration
		}
		return gaps[i].Start.Before(gaps[j].Start)
	})
	if top > 0 && len(gaps) > top {
		gaps = gaps[:top]
	}
	return gaps
}

// appendGap adds a [start, end) gap if it is positive-length. Zero/negative
// spans (adjacent busy minutes) are dropped.
func appendGap(gaps []Gap, start, end time.Time) []Gap {
	if !end.After(start) {
		return gaps
	}
	return append(gaps, Gap{Start: start, End: end, Duration: end.Sub(start)})
}
