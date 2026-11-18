// Package coverage compares desired fire times against the union of a crontab's
// jobs. Matching is by exact instant, not by average frequency or job duration.
package coverage

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rwrife/cron-goblin/internal/english"
	"github.com/rwrife/cron-goblin/internal/lint"
	"github.com/rwrife/cron-goblin/internal/nextrun"
	"github.com/rwrife/cron-goblin/internal/parse"
)

const DefaultWindow = 7 * 24 * time.Hour

type Gap struct {
	Time time.Time `json:"time"`
}

type Redundancy struct {
	Time  time.Time `json:"time"`
	Count int       `json:"count"`
	Lines []int     `json:"lines"`
}

type Report struct {
	Want         string       `json:"want"`
	Timezone     string       `json:"timezone"`
	From         time.Time    `json:"from"`
	To           time.Time    `json:"to"`
	Status       string       `json:"status"`
	Expected     int          `json:"expected"`
	Covered      int          `json:"covered"`
	Gaps         []Gap        `json:"gaps"`
	Redundancies []Redundancy `json:"redundancies"`
}

// Analyze considers fire times in (from, from+window]. A gap is a missing desired
// fire; a redundancy identifies all source lines firing at the same desired
// instant. Fires outside the desired cadence neither fill gaps nor count as
// redundancies. Malformed lines are errors rather than silently ignored jobs.
func Analyze(src, want string, from time.Time, window time.Duration, loc *time.Location) (Report, error) {
	if window <= 0 {
		return Report{}, fmt.Errorf("analysis window must be positive")
	}
	if loc == nil {
		loc = time.UTC
	}
	schedule, err := parse.Parse(want)
	if err != nil {
		expr, englishErr := english.Parse(want)
		if englishErr != nil {
			return Report{}, fmt.Errorf("--want must be cron or supported English: %w", englishErr)
		}
		schedule, err = parse.Parse(expr)
		if err != nil {
			return Report{}, fmt.Errorf("invalid translated target: %w", err)
		}
	}
	entries, err := lint.ParseCrontab(strings.NewReader(src))
	if err != nil {
		return Report{}, fmt.Errorf("reading crontab: %w", err)
	}
	for _, entry := range entries {
		if entry.ParseErr != nil {
			return Report{}, fmt.Errorf("crontab line %d: %w", entry.Line, entry.ParseErr)
		}
	}

	rep := Report{
		Want: schedule.Raw, Timezone: loc.String(),
		From: from.In(loc), To: from.Add(window).In(loc),
		Status: "covered", Gaps: []Gap{}, Redundancies: []Redundancy{},
	}
	targets := []time.Time{}
	err = walk(schedule, rep.From, rep.To, loc, func(t time.Time) {
		targets = append(targets, t)
	})
	if err != nil {
		return Report{}, fmt.Errorf("target schedule: %w", err)
	}
	rep.Expected = len(targets)
	lines := make(map[int64][]int, len(targets))
	for _, t := range targets {
		lines[t.Unix()] = nil
	}
	if len(targets) > 0 {
		for _, entry := range entries {
			err := walk(entry.Schedule, rep.From, targets[len(targets)-1], loc, func(t time.Time) {
				if _, desired := lines[t.Unix()]; desired {
					lines[t.Unix()] = append(lines[t.Unix()], entry.Line)
				}
			})
			if err != nil {
				return Report{}, fmt.Errorf("crontab line %d: %w", entry.Line, err)
			}
		}
	}
	for _, t := range targets {
		jobs := lines[t.Unix()]
		if len(jobs) == 0 {
			rep.Gaps = append(rep.Gaps, Gap{Time: t})
			continue
		}
		rep.Covered++
		if len(jobs) > 1 {
			rep.Redundancies = append(rep.Redundancies, Redundancy{Time: t, Count: len(jobs), Lines: jobs})
		}
	}
	if len(rep.Gaps) > 0 {
		rep.Status = "under-covered"
	} else if len(rep.Redundancies) > 0 {
		rep.Status = "over-covered"
	}
	return rep, nil
}

func walk(s parse.Schedule, from, to time.Time, loc *time.Location, visit func(time.Time)) error {
	for cur := from; cur.Before(to); {
		next, err := nextrun.Next(s, cur, loc)
		if errors.Is(err, nextrun.ErrNeverFires) {
			return nil
		}
		if err != nil {
			return err
		}
		if !next.After(cur) {
			return fmt.Errorf("fire-time iterator did not advance after %s", cur.Format(time.RFC3339))
		}
		if next.After(to) {
			return nil
		}
		visit(next)
		cur = next
	}
	return nil
}
