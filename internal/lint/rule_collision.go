// rule_collision.go implements collision detection across jobs — the
// "thundering herd" seed. When several crontab entries fire at the very same
// instant, they stampede your machine together: the classic 3am backup +
// log-rotate + report-build pile-up. We warn and name the colliding lines so
// the user can stagger them (an auto-stagger suggestion is backlog).
//
// Detection is by shared fire *instant*, computed from the trusted nextrun
// engine in UTC. Two jobs collide when any of their upcoming fire times (within
// a small sampling window) coincide to the minute. Sampling real fire times —
// rather than string-matching fields — means we correctly catch collisions
// that arise from ranges and steps (e.g. `*/30 * * * *` vs `0 * * * *` both
// hitting the top of the hour) and never false-alarm on jobs that merely share
// a field but never actually align.
package lint

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/rwrife/cron-goblin/internal/nextrun"
)

// collisionRule flags groups of entries that fire at the same instant.
type collisionRule struct{}

// Name returns the stable rule code.
func (collisionRule) Name() string { return "collision" }

// collisionSamples is how many upcoming fire times we sample per entry when
// looking for coincidences. A window of a few dozen comfortably surfaces daily
// and hourly pile-ups while staying cheap; rarer alignments (e.g. monthly) are
// caught when their sampled windows overlap.
const collisionSamples = 32

// Check returns one warning per distinct instant at which two or more entries
// fire together. Each finding lists the colliding source lines. Entries that
// failed to parse or never fire contribute nothing.
func (collisionRule) Check(entries []Entry) []Finding {
	from := time.Now().Truncate(time.Minute)

	// Map each fire instant (as a stable RFC3339 key) to the set of entry line
	// numbers that hit it.
	type hit struct {
		when  time.Time
		lines []int
	}
	byInstant := map[string]*hit{}

	for _, e := range entries {
		if e.ParseErr != nil {
			continue
		}
		runs := nextrun.NextN(e.Schedule, from, collisionSamples, time.UTC)
		for _, t := range runs {
			key := t.Format(time.RFC3339)
			h := byInstant[key]
			if h == nil {
				h = &hit{when: t}
				byInstant[key] = h
			}
			// Guard against the same line being counted twice for one instant
			// (can't happen with NextN's strictly-increasing output, but cheap
			// insurance keeps the rule robust to future changes).
			if len(h.lines) == 0 || h.lines[len(h.lines)-1] != e.Line {
				h.lines = append(h.lines, e.Line)
			}
		}
	}

	// Collect only instants with 2+ distinct entries. Dedupe by the exact set
	// of colliding lines so a daily 3am stampede is reported once, not once per
	// sampled day.
	seenGroups := map[string]bool{}
	var out []Finding

	// Deterministic iteration: sort instant keys chronologically.
	keys := make([]string, 0, len(byInstant))
	for k := range byInstant {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return byInstant[keys[i]].when.Before(byInstant[keys[j]].when)
	})

	for _, k := range keys {
		h := byInstant[k]
		lines := dedupeInts(h.lines)
		if len(lines) < 2 {
			continue
		}
		groupKey := joinInts(lines)
		if seenGroups[groupKey] {
			continue
		}
		seenGroups[groupKey] = true

		out = append(out, Finding{
			Rule:     "collision",
			Severity: SeverityWarning,
			Message: fmt.Sprintf(
				"%d jobs fire at the same time (e.g. %s UTC) — a thundering herd on lines %s; consider staggering them",
				len(lines), h.when.Format("2006-01-02 15:04"), joinIntsHuman(lines)),
			Lines: lines,
		})
	}
	return out
}

// dedupeInts returns the sorted, de-duplicated set of the given ints.
func dedupeInts(in []int) []int {
	set := map[int]struct{}{}
	for _, v := range in {
		set[v] = struct{}{}
	}
	out := make([]int, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Ints(out)
	return out
}

// joinInts renders ints as a compact comma key (for map deduping).
func joinInts(in []int) string {
	parts := make([]string, len(in))
	for i, v := range in {
		parts[i] = fmt.Sprintf("%d", v)
	}
	return strings.Join(parts, ",")
}

// joinIntsHuman renders line numbers for a message, e.g. "3, 7 and 12".
func joinIntsHuman(in []int) string {
	switch len(in) {
	case 0:
		return ""
	case 1:
		return fmt.Sprintf("%d", in[0])
	}
	head := make([]string, len(in)-1)
	for i := 0; i < len(in)-1; i++ {
		head[i] = fmt.Sprintf("%d", in[i])
	}
	return fmt.Sprintf("%s and %d", strings.Join(head, ", "), in[len(in)-1])
}
