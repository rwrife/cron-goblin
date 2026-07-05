// diff.go implements `goblin diff`: compare how two cron schedules fire, so you
// can see exactly what shifts before you commit a crontab edit. It reuses the
// M3 nextrun engine to materialize each schedule's upcoming fire times, then
// takes the set difference over a shared window (either the next N runs of each,
// or every run inside a duration window).
//
// The output answers one question: "if I change FROM this TO that, which runs
// appear, which disappear, and which stay put?" — with a `--json` shape for
// review tooling and agents.
package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/rwrife/cron-goblin/internal/explain"
	"github.com/rwrife/cron-goblin/internal/goblin"
	"github.com/rwrife/cron-goblin/internal/nextrun"
	"github.com/rwrife/cron-goblin/internal/parse"
	"github.com/spf13/cobra"
)

// diffKind labels a single fire time's fate when moving from old to new.
type diffKind string

const (
	diffSame    diffKind = "same"    // fires in both schedules at this instant
	diffAdded   diffKind = "added"   // fires in the new schedule only
	diffRemoved diffKind = "removed" // fired in the old schedule only
)

// diffEntry is one instant on the merged timeline, tagged with which side(s)
// fire there.
type diffEntry struct {
	Time time.Time
	Kind diffKind
}

// diffJSON is the machine-readable shape emitted by `diff --json`. It is stable
// and small so agents/scripts can depend on it: the two expressions, the window
// that was compared, and the classified timeline plus roll-up counts.
type diffJSON struct {
	Old        string          `json:"old"`
	New        string          `json:"new"`
	OldEnglish string          `json:"old_english"`
	NewEnglish string          `json:"new_english"`
	Timezone   string          `json:"timezone"`
	Window     string          `json:"window"`
	Added      []string        `json:"added"`
	Removed    []string        `json:"removed"`
	Unchanged  []string        `json:"unchanged"`
	Timeline   []diffJSONEntry `json:"timeline"`
	Summary    diffSummary     `json:"summary"`
}

// diffJSONEntry is one timeline instant in the JSON output.
type diffJSONEntry struct {
	Time string   `json:"time"`
	Kind diffKind `json:"kind"`
}

// diffSummary rolls up the counts so a reviewer (or a CI gate) can decide at a
// glance whether an edit is a no-op, a pure addition, etc.
type diffSummary struct {
	Added     int  `json:"added"`
	Removed   int  `json:"removed"`
	Unchanged int  `json:"unchanged"`
	Identical bool `json:"identical"` // true when nothing was added or removed
}

// newDiffCmd builds the `diff` subcommand.
func newDiffCmd() *cobra.Command {
	var (
		asJSON bool
		count  int
		tz     string
		window string
		quiet  bool
	)

	cmd := &cobra.Command{
		Use:   "diff <old-expression> <new-expression>",
		Short: "Show how fire times change between two cron schedules",
		Long: "Compare two standard 5-field cron expressions and show exactly which\n" +
			"runs appear, disappear, or stay put when you change from the first to\n" +
			"the second. Perfect for sanity-checking a crontab edit before you\n" +
			"commit it.\n\n" +
			"By default the next N runs of each schedule (-n, default 10) are lined\n" +
			"up and compared. Pass --window to instead compare every run inside a\n" +
			"duration from now (for example --window 7d or --window 48h); the two\n" +
			"modes are mutually exclusive.\n\n" +
			"Runs are matched to the minute in the chosen timezone (--tz). Output is\n" +
			"a merged timeline: '=' unchanged, '+' added by the new schedule, '-'\n" +
			"removed by it. Pass --json for a machine-readable diff.",
		Example: "  goblin diff \"0 9 * * *\" \"30 9 * * *\"\n" +
			"  goblin diff -n 20 \"*/15 * * * *\" \"*/30 * * * *\"\n" +
			"  goblin diff --window 7d --tz America/New_York \"0 9 * * 1-5\" \"0 8 * * 1-5\"\n" +
			"  goblin diff --json \"0 0 * * *\" \"0 0 * * 1-5\"",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			oldExpr, newExpr := args[0], args[1]

			if window != "" && cmd.Flags().Changed("count") {
				err := fmt.Errorf("--window and -n/--count are mutually exclusive; pick a run count OR a time window")
				fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", err)
				return err
			}
			if window == "" && count <= 0 {
				err := fmt.Errorf("count (-n) must be positive, got %d", count)
				fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", err)
				return err
			}

			var win time.Duration
			if window != "" {
				var derr error
				win, derr = parseWindow(window)
				if derr != nil {
					if !quiet {
						fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line(window))
					}
					fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", derr)
					return derr
				}
			}

			loc, err := loadLocation(tz)
			if err != nil {
				if !quiet {
					fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line(tz))
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "error: unknown timezone %q: %v\n", tz, err)
				return err
			}

			oldSched, err := parse.Parse(oldExpr)
			if err != nil {
				if !quiet {
					fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line(oldExpr))
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "error: old expression: %v\n", err)
				return err
			}
			newSched, err := parse.Parse(newExpr)
			if err != nil {
				if !quiet {
					fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line(newExpr))
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "error: new expression: %v\n", err)
				return err
			}

			now := time.Now()
			var oldRuns, newRuns []time.Time
			if window != "" {
				oldRuns = runsInWindow(oldSched, now, win, loc)
				newRuns = runsInWindow(newSched, now, win, loc)
			} else {
				oldRuns = nextrun.NextN(oldSched, now, count, loc)
				newRuns = nextrun.NextN(newSched, now, count, loc)
			}

			timeline := mergeTimelines(oldRuns, newRuns)

			oldEnglish := explain.Explain(oldSched)
			newEnglish := explain.Explain(newSched)
			windowLabel := describeWindow(window, count)

			if asJSON {
				return emitDiffJSON(cmd, diffInputs{
					oldSched:    oldSched,
					newSched:    newSched,
					oldEnglish:  oldEnglish,
					newEnglish:  newEnglish,
					loc:         loc,
					windowLabel: windowLabel,
					timeline:    timeline,
				})
			}

			return emitDiffHuman(cmd, quiet, diffInputs{
				oldExpr:     oldExpr,
				newExpr:     newExpr,
				oldSched:    oldSched,
				newSched:    newSched,
				oldEnglish:  oldEnglish,
				newEnglish:  newEnglish,
				loc:         loc,
				windowLabel: windowLabel,
				timeline:    timeline,
			})
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "emit a machine-readable JSON diff")
	cmd.Flags().IntVarP(&count, "count", "n", 10, "how many upcoming runs of each schedule to compare")
	cmd.Flags().StringVar(&tz, "tz", "", "timezone for fire times (IANA name, e.g. America/New_York; default: local)")
	cmd.Flags().StringVar(&window, "window", "", "compare every run within a duration from now instead of a run count (e.g. 7d, 48h, 90m)")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "silence the goblin's grumbling (stderr persona)")

	return cmd
}

// diffInputs bundles the values both output paths need, keeping the RunE body
// readable and the two emitters honest about what they depend on.
type diffInputs struct {
	oldExpr     string
	newExpr     string
	oldSched    parse.Schedule
	newSched    parse.Schedule
	oldEnglish  string
	newEnglish  string
	loc         *time.Location
	windowLabel string
	timeline    []diffEntry
}

// mergeTimelines set-differences two sorted-or-unsorted fire-time slices into a
// single chronological timeline, tagging each instant as same/added/removed.
// Matching is to the exact instant (both engines already truncate to the
// minute), so equal wall-clock minutes in the same location coincide.
func mergeTimelines(oldRuns, newRuns []time.Time) []diffEntry {
	inOld := make(map[int64]bool, len(oldRuns))
	for _, t := range oldRuns {
		inOld[t.Unix()] = true
	}
	inNew := make(map[int64]bool, len(newRuns))
	for _, t := range newRuns {
		inNew[t.Unix()] = true
	}

	// Union of instants, de-duplicated, carrying one representative time value
	// (they're equal by Unix key, so either side's value is fine).
	seen := make(map[int64]time.Time)
	for _, t := range oldRuns {
		seen[t.Unix()] = t
	}
	for _, t := range newRuns {
		if _, ok := seen[t.Unix()]; !ok {
			seen[t.Unix()] = t
		}
	}

	out := make([]diffEntry, 0, len(seen))
	for key, t := range seen {
		var kind diffKind
		switch {
		case inOld[key] && inNew[key]:
			kind = diffSame
		case inNew[key]:
			kind = diffAdded
		default:
			kind = diffRemoved
		}
		out = append(out, diffEntry{Time: t, Kind: kind})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out
}

// runsInWindow returns every fire time strictly after `from` and no later than
// from+window, in loc. It walks the nextrun engine one fire at a time so sparse
// and dense schedules alike terminate at the window edge rather than a count.
func runsInWindow(s parse.Schedule, from time.Time, window time.Duration, loc *time.Location) []time.Time {
	if loc == nil {
		loc = time.UTC
	}
	deadline := from.Add(window)
	out := []time.Time{}
	cur := from
	// A generous cap guards against a pathological sub-minute window being
	// combined with an every-minute schedule; in practice the deadline check
	// ends the loop first.
	const hardCap = 100000
	for i := 0; i < hardCap; i++ {
		t, err := nextrun.Next(s, cur, loc)
		if err != nil {
			break // never fires (again) within the horizon
		}
		if t.After(deadline) {
			break
		}
		out = append(out, t)
		cur = t
	}
	return out
}

// emitDiffJSON writes the stable JSON diff.
func emitDiffJSON(cmd *cobra.Command, in diffInputs) error {
	added := make([]string, 0)
	removed := make([]string, 0)
	unchanged := make([]string, 0)
	entries := make([]diffJSONEntry, 0, len(in.timeline))
	for _, e := range in.timeline {
		iso := isoStable(e.Time)
		entries = append(entries, diffJSONEntry{Time: iso, Kind: e.Kind})
		switch e.Kind {
		case diffAdded:
			added = append(added, iso)
		case diffRemoved:
			removed = append(removed, iso)
		case diffSame:
			unchanged = append(unchanged, iso)
		}
	}

	payload := diffJSON{
		Old:        in.oldSched.Raw,
		New:        in.newSched.Raw,
		OldEnglish: in.oldEnglish,
		NewEnglish: in.newEnglish,
		Timezone:   in.loc.String(),
		Window:     in.windowLabel,
		Added:      added,
		Removed:    removed,
		Unchanged:  unchanged,
		Timeline:   entries,
		Summary: diffSummary{
			Added:     len(added),
			Removed:   len(removed),
			Unchanged: len(unchanged),
			Identical: len(added) == 0 && len(removed) == 0,
		},
	}
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(payload)
}

// emitDiffHuman writes the grumble-on-stderr, facts-on-stdout human view.
func emitDiffHuman(cmd *cobra.Command, quiet bool, in diffInputs) error {
	if !quiet {
		fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line(in.oldExpr+in.newExpr))
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "- %s  (%s)\n", in.oldSched.Raw, in.oldEnglish)
	fmt.Fprintf(out, "+ %s  (%s)\n", in.newSched.Raw, in.newEnglish)
	fmt.Fprintf(out, "\nComparing %s in %s:\n\n", in.windowLabel, in.loc.String())

	var added, removed, same int
	if len(in.timeline) == 0 {
		fmt.Fprintln(out, "  (neither schedule fires in this window)")
	}
	for _, e := range in.timeline {
		var marker string
		switch e.Kind {
		case diffAdded:
			marker = "+"
			added++
		case diffRemoved:
			marker = "-"
			removed++
		default:
			marker = "="
			same++
		}
		fmt.Fprintf(out, "  %s %s\n", marker, isoStable(e.Time))
	}

	fmt.Fprintf(out, "\n%d added, %d removed, %d unchanged.\n", added, removed, same)
	if added == 0 && removed == 0 && len(in.timeline) > 0 {
		fmt.Fprintln(out, "No change in fire times over this window — the edit is a no-op here.")
	}
	return nil
}

// parseWindow accepts a friendly duration string. It extends Go's
// time.ParseDuration with a 'd' (day = 24h) suffix, because "next 7 days" is the
// natural way to ask for a crontab-review window and stdlib refuses 'd'. Plain
// Go durations ("48h", "90m", "36h30m") still work. The result must be
// positive.
func parseWindow(s string) (time.Duration, error) {
	// Handle a trailing whole-day suffix like "7d" or "10d". We only special-
	// case a bare "<int>d"; compound forms should use hours/minutes.
	if n := len(s); n >= 2 && (s[n-1] == 'd' || s[n-1] == 'D') {
		daysPart := s[:n-1]
		days, err := parsePositiveInt(daysPart)
		if err == nil {
			return time.Duration(days) * 24 * time.Hour, nil
		}
		// Fall through to ParseDuration for anything not a clean "<int>d".
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid --window %q: use a duration like 7d, 48h, or 90m", s)
	}
	if d <= 0 {
		return 0, fmt.Errorf("--window must be positive, got %q", s)
	}
	return d, nil
}

// parsePositiveInt parses a base-10 non-negative integer without pulling in
// strconv error phrasing; it returns an error for empty or non-digit input.
func parsePositiveInt(s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("not a whole number: %q", s)
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}

// describeWindow produces the human/JSON label for the comparison span, so both
// emitters agree on wording.
func describeWindow(window string, count int) string {
	if window != "" {
		return "runs in the next " + window
	}
	return fmt.Sprintf("the next %d run(s) of each", count)
}
