// rule_dst.go implements the DST-danger rule: it flags schedules whose intended
// wall-clock fire time lands in a daylight-saving transition window for a chosen
// timezone, where the job will be silently skipped (spring-forward gap) or fire
// at an ambiguous instant (fall-back overlap).
//
// Why this matters: cron-goblin's nextrun engine already does the *right thing*
// at a transition — it skips the missing hour and fires an overlapped time only
// once — but that "right thing" is often a surprise. A nightly job pinned to
// 02:30 in America/New_York simply does not run on the spring-forward day, and
// a job at 01:30 on the fall-back day runs at a moment that exists twice. The
// rule's job is to *warn the author at design time* and suggest a safer slot.
//
// The rule needs a timezone to mean anything (UTC and other no-DST zones never
// trigger it), so unlike the always-on default rules it is constructed with a
// *time.Location and only contributes findings when one is supplied. See
// DefaultRulesTZ / LintWithLocation and the TUI's CheckScheduleTZ.
package lint

import (
	"fmt"
	"time"

	"github.com/rwrife/cron-goblin/internal/parse"
)

// dstDangerRule flags schedules that fire inside a DST transition window. loc is
// the timezone whose transitions are considered; years is the set of calendar
// years to examine (typically the current year and the next, so an imminent
// transition is caught even late in the year).
type dstDangerRule struct {
	loc   *time.Location
	years []int
}

// Name returns the stable rule code.
func (dstDangerRule) Name() string { return "dst-danger" }

// newDSTDangerRule builds the rule for a location, examining the year of `now`
// and the following year. A nil or UTC location yields a rule that never fires
// (its Check returns nothing), so callers can construct it unconditionally.
func newDSTDangerRule(loc *time.Location, now time.Time) dstDangerRule {
	y := now.Year()
	return dstDangerRule{loc: loc, years: []int{y, y + 1}}
}

// Check examines every parseable entry and emits a finding when the schedule
// can fire inside a gap (skipped) or overlap (ambiguous) window in loc. A
// schedule is reported at most once per transition kind so a daily 02:30 job
// yields one "skipped" finding, not one per affected year.
func (r dstDangerRule) Check(entries []Entry) []Finding {
	if r.loc == nil || r.loc == time.UTC {
		return nil
	}

	// Gather the transitions once for all examined years.
	var trans []dstTransition
	for _, y := range r.years {
		trans = append(trans, dstTransitions(r.loc, y)...)
	}
	if len(trans) == 0 {
		return nil // zone has no DST in range; nothing to warn about.
	}

	var out []Finding
	for _, e := range entries {
		if e.ParseErr != nil {
			continue
		}
		gapHit, overlapHit := false, false
		var gapWin, overlapWin dstTransition

		for _, tr := range trans {
			if !scheduleHitsWindow(e.Schedule, tr) {
				continue
			}
			switch tr.kind {
			case kindGap:
				if !gapHit {
					gapHit, gapWin = true, tr
				}
			case kindOverlap:
				if !overlapHit {
					overlapHit, overlapWin = true, tr
				}
			}
		}

		if gapHit {
			out = append(out, Finding{
				Rule:     "dst-danger",
				Severity: SeverityWarning,
				Message: fmt.Sprintf(
					"`%s` fires during the spring-forward gap (%s, the missing %s–%s in %s) — that run is silently skipped; pick a time outside %s",
					e.Schedule.Raw,
					gapWin.date.Format("2006-01-02"),
					minLabel(gapWin.startMin), minLabel(gapWin.endMin),
					r.loc.String(),
					windowLabel(gapWin)),
				Lines: lineSlice(e.Line),
			})
		}
		if overlapHit {
			out = append(out, Finding{
				Rule:     "dst-danger",
				Severity: SeverityInfo,
				Message: fmt.Sprintf(
					"`%s` fires during the fall-back overlap (%s, the repeated %s–%s in %s) — that wall-clock time happens twice; the job runs once (at the first occurrence), which may not be what you intend",
					e.Schedule.Raw,
					overlapWin.date.Format("2006-01-02"),
					minLabel(overlapWin.startMin), minLabel(overlapWin.endMin),
					r.loc.String()),
				Lines: lineSlice(e.Line),
			})
		}
	}
	return out
}

// scheduleHitsWindow reports whether s can fire on the transition's local date
// at a wall-clock time inside the transition window. It checks month and the
// cron DOM/DOW OR-rule for the date, then whether any matching (hour, minute)
// pair falls in [startMin, endMin).
//
// This evaluates the schedule's *intent* against wall-clock fields directly —
// deliberately not via the nextrun engine, which would have already skipped the
// gap or collapsed the overlap, hiding exactly what we want to flag.
func scheduleHitsWindow(s parse.Schedule, tr dstTransition) bool {
	d := tr.date
	if !contains(s.Month.Values, int(d.Month())) {
		return false
	}
	if !dayMatches(s, d) {
		return false
	}
	for _, h := range s.Hour.Values {
		for _, m := range s.Minute.Values {
			if tr.affects(h, m) {
				return true
			}
		}
	}
	return false
}

// dayMatches applies cron's day-of-month / day-of-week OR-rule to a calendar
// day, mirroring nextrun's semantics so the rule and the engine never disagree
// about which days a schedule covers. A field counts as restricted when it was
// not written as a bare "*".
func dayMatches(s parse.Schedule, d time.Time) bool {
	domOK := contains(s.DOM.Values, d.Day())
	dowOK := contains(s.DOW.Values, int(d.Weekday()))
	domRestricted := !s.DOM.Star
	dowRestricted := !s.DOW.Star

	switch {
	case domRestricted && dowRestricted:
		return domOK || dowOK
	case domRestricted:
		return domOK
	case dowRestricted:
		return dowOK
	default:
		return true
	}
}

// contains reports membership of v in a sorted-or-unsorted int slice. The cron
// value sets are small (≤60), so a linear scan is plenty.
func contains(vals []int, v int) bool {
	for _, x := range vals {
		if x == v {
			return true
		}
	}
	return false
}

// minLabel formats a minutes-from-midnight value as a local "HH:MM" wall-clock
// label for messages.
func minLabel(mins int) string {
	return fmt.Sprintf("%02d:%02d", mins/60, mins%60)
}

// windowLabel renders the affected window of a transition as "HH:MM–HH:MM
// local" for the "pick a time outside ..." suggestion.
func windowLabel(tr dstTransition) string {
	return fmt.Sprintf("%s–%s local", minLabel(tr.startMin), minLabel(tr.endMin))
}

// lineSlice wraps a single line number in a slice, or returns an empty slice
// for the synthetic line 0 used by single-schedule checks (so JSON shows []
// rather than [0]).
func lineSlice(line int) []int {
	if line <= 0 {
		return []int{}
	}
	return []int{line}
}
