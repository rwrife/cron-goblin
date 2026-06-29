// dst.go provides the daylight-saving-time transition math the DST-danger rule
// is built on. It is deliberately separate from the rule so the tricky calendar
// arithmetic can be unit-tested in isolation, the way the rest of cron-goblin
// keeps logic pure and humorless.
//
// # What a transition is
//
// A DST transition is an instant at which a location's UTC offset changes:
//
//   - Spring-forward (a "gap"): the local clock jumps *forward*, e.g. in
//     America/New_York 02:00 becomes 03:00 on the second Sunday of March. The
//     wall-clock interval [02:00, 03:00) never happens — a job scheduled at
//     02:30 that day is silently skipped.
//   - Fall-back (an "overlap"): the local clock jumps *backward*, e.g. 02:00
//     becomes 01:00 on the first Sunday of November. The wall-clock interval
//     [01:00, 02:00) happens *twice* — a job scheduled at 01:30 fires at an
//     ambiguous instant (and most cron implementations, including this tool's
//     nextrun engine, fire it only once rather than twice).
//
// # How we find them
//
// Go's time package knows each zone's offset at any instant but does not expose
// a transition list. We recover transitions by scanning a year at hourly
// resolution for offset changes, then binary-searching each change down to the
// exact second. From the before/after offsets we derive the affected *local
// wall-clock* window, which is what the rule tests fire times against.
package lint

import (
	"sort"
	"sync"
	"time"
)

// transitionKind distinguishes the two ways a DST change can hurt a schedule.
type transitionKind int

const (
	// kindGap is a spring-forward: a window of local wall-clock time that does
	// not exist. Jobs scheduled inside it never fire on that day.
	kindGap transitionKind = iota
	// kindOverlap is a fall-back: a window of local wall-clock time that occurs
	// twice. Jobs scheduled inside it fire at an ambiguous instant.
	kindOverlap
)

// dstTransition describes one offset change in a location, reduced to the
// information the rule needs: the kind, and the affected local wall-clock
// window expressed as [startMin, endMin) minutes-of-day on the transition date.
//
// The window is half-open and always within a single local calendar day (DST
// shifts are an hour or two and never straddle midnight in practice, and even
// if a zone did something exotic we clamp to the day). For a gap, [start, end)
// is the missing hour(s); for an overlap, it is the repeated hour(s).
type dstTransition struct {
	kind transitionKind
	// date is the local calendar day (midnight in loc) the transition falls on.
	date time.Time
	// startMin and endMin bound the affected wall-clock window as minutes from
	// local midnight on date: [startMin, endMin). For a gap these are the wall
	// times that do not exist; for an overlap, the wall times that repeat.
	startMin int
	endMin   int
	// delta is the size of the shift (always positive), e.g. one hour.
	delta time.Duration
}

// affects reports whether a fire time at the given local hour/minute on the
// transition's date lands inside the affected window. hour/minute are the
// schedule's intended wall-clock fields.
func (t dstTransition) affects(hour, minute int) bool {
	mod := hour*60 + minute
	return mod >= t.startMin && mod < t.endMin
}

// dstTransitions returns every DST transition for loc during the given year,
// chronologically. A location with no DST (e.g. UTC, Asia/Kolkata) yields an
// empty slice. The result is cached per (zone-name, year) since transitions are
// fixed and the scan, while cheap, runs once per linted schedule.
func dstTransitions(loc *time.Location, year int) []dstTransition {
	if loc == nil || loc == time.UTC {
		return nil
	}
	key := dstCacheKey{name: loc.String(), year: year}
	if v, ok := dstCacheGet(key); ok {
		return v
	}
	out := computeDSTTransitions(loc, year)
	dstCachePut(key, out)
	return out
}

// computeDSTTransitions does the actual scan-and-refine work for one year.
func computeDSTTransitions(loc *time.Location, year int) []dstTransition {
	// Scan the whole year in UTC at hourly steps, watching for the local UTC
	// offset to change between consecutive samples. Year boundaries are padded
	// by an hour on each side so a transition exactly at the edge is still
	// bracketed.
	start := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC).Add(-time.Hour)
	end := time.Date(year+1, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Hour)

	var out []dstTransition
	prev := start
	_, prevOff := prev.In(loc).Zone()

	for cur := start.Add(time.Hour); !cur.After(end); cur = cur.Add(time.Hour) {
		_, curOff := cur.In(loc).Zone()
		if curOff != prevOff {
			// An offset change happened in (prev, cur]. Pin it to the exact
			// second, then characterize the window.
			instant := refineTransition(loc, prev, cur, prevOff)
			if tr, ok := classify(loc, instant, year); ok {
				out = append(out, tr)
			}
		}
		prev, prevOff = cur, curOff
	}

	sort.Slice(out, func(i, j int) bool { return out[i].date.Before(out[j].date) })
	return out
}

// refineTransition binary-searches the half-open interval (lo, hi] for the exact
// instant at which loc's offset stops being beforeOff. lo is known to have
// offset beforeOff; hi is known to differ. The returned instant is the first
// second at the new offset.
func refineTransition(loc *time.Location, lo, hi time.Time, beforeOff int) time.Time {
	for hi.Sub(lo) > time.Second {
		mid := lo.Add(hi.Sub(lo) / 2)
		_, off := mid.In(loc).Zone()
		if off == beforeOff {
			lo = mid
		} else {
			hi = mid
		}
	}
	return hi
}

// classify turns a precise transition instant into a dstTransition with its
// affected local wall-clock window. instant is the first UTC second at the new
// offset. It returns ok=false if the transition's local date falls outside the
// requested year (can happen for the padding instants at a year edge).
func classify(loc *time.Location, instant time.Time, year int) (dstTransition, bool) {
	// Offsets immediately before and after the change, both measured *in loc*
	// (instant comes from refineTransition as a UTC time, so we must convert
	// before asking for the zone offset).
	_, afterOff := instant.In(loc).Zone()
	_, beforeOff := instant.Add(-time.Second).In(loc).Zone()
	delta := time.Duration(afterOff-beforeOff) * time.Second

	// The local wall-clock instant *just before* the jump anchors the window.
	// In spring-forward, local time leaps from this point forward by +delta; in
	// fall-back it leaps backward by -delta (delta negative). We normalize to a
	// positive window size.
	beforeLocal := instant.Add(-time.Second).In(loc)
	// The wall clock the instant after the jump (what the clock reads now).
	afterLocal := instant.In(loc)

	tr := dstTransition{}
	switch {
	case delta > 0:
		// Gap: wall times in [afterLocal-delta? no — the missing window is
		// [old-wall-at-jump, new-wall-at-jump). At the jump the clock reads
		// `beforeLocal`'s next second logically, but physically becomes
		// afterLocal. The non-existent interval is [beforeWall, afterWall) where
		// beforeWall is the last existing wall second +1s and afterWall is the
		// new reading. Concretely for US spring-forward the missing hour is
		// [02:00, 03:00): beforeLocal is 01:59:59, afterLocal is 03:00:00.
		tr.kind = kindGap
		startMin := (beforeLocal.Hour()*60 + beforeLocal.Minute()) + 1 // 02:00
		// startMin computed from 01:59 +1min = 02:00; clamp into day.
		endMin := afterLocal.Hour()*60 + afterLocal.Minute() // 03:00
		tr.date = dayStart(beforeLocal)
		tr.startMin, tr.endMin = clampWindow(startMin, endMin)
		tr.delta = delta
	case delta < 0:
		// Overlap: the wall-clock window that repeats is [afterLocal,
		// afterLocal-delta) i.e. [01:00, 02:00) for US fall-back. afterLocal is
		// 01:00:00 (the second reading), and the window spans |delta|.
		tr.kind = kindOverlap
		size := int((-delta) / time.Minute)
		startMin := afterLocal.Hour()*60 + afterLocal.Minute() // 01:00
		endMin := startMin + size                              // 02:00
		tr.date = dayStart(afterLocal)
		tr.startMin, tr.endMin = clampWindow(startMin, endMin)
		tr.delta = -delta
	default:
		return dstTransition{}, false
	}

	if tr.date.Year() != year {
		return dstTransition{}, false
	}
	return tr, true
}

// dayStart returns local midnight for t's calendar day in t's location.
func dayStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// clampWindow constrains a [start, end) minute window to a single day [0,1440)
// and guarantees start < end. Real DST shifts never need clamping; this is
// defensive against exotic zones.
func clampWindow(start, end int) (int, int) {
	if start < 0 {
		start = 0
	}
	if end > 24*60 {
		end = 24 * 60
	}
	if end <= start {
		end = start + 1
	}
	return start, end
}

// --- transition cache -------------------------------------------------------

// dstCacheKey identifies a cached transition list. Transitions are a pure
// function of zone name and year, so this is a safe, stable key.
type dstCacheKey struct {
	name string
	year int
}

// dstCache memoizes computed transitions. The DST rule may ask about the same
// (zone, year) once per linted schedule, and the TUI recomputes on every
// keystroke; caching keeps that free. Access is guarded so the rule stays safe
// if a caller ever lints concurrently.
var (
	dstCache   = map[dstCacheKey][]dstTransition{}
	dstCacheMu sync.Mutex
)

func dstCacheGet(k dstCacheKey) ([]dstTransition, bool) {
	dstCacheMu.Lock()
	defer dstCacheMu.Unlock()
	v, ok := dstCache[k]
	return v, ok
}

func dstCachePut(k dstCacheKey, v []dstTransition) {
	dstCacheMu.Lock()
	defer dstCacheMu.Unlock()
	dstCache[k] = v
}
