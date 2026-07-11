// gaps.go implements `goblin gaps`: across all jobs in a crontab, find the
// longest stretches of time where nothing is scheduled to fire over a
// look-ahead window. It is the inverse of `stagger`'s thundering-herd view —
// instead of "too much at once", it answers "when is this box actually quiet?"
// — the natural place to slot a heavy backup, a deploy freeze, or a maintenance
// reboot. The math is pure (internal/gaps); this file is just the CLI surface.
package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/rwrife/cron-goblin/internal/gaps"
	"github.com/rwrife/cron-goblin/internal/goblin"
	"github.com/spf13/cobra"
)

// gapJSON is the stable per-gap shape in `gaps --json`.
type gapJSON struct {
	Start           string `json:"start"`
	End             string `json:"end"`
	DurationSeconds int64  `json:"duration_seconds"`
}

// busiestJSON is the busiest-minute block in `gaps --json`.
type busiestJSON struct {
	Time  string `json:"time"`
	Count int    `json:"count"`
}

// gapsReportJSON is the machine-readable shape emitted by `gaps --json`. Small
// and stable so agents/CI can depend on it.
type gapsReportJSON struct {
	Source  string      `json:"source"`
	From    string      `json:"from"`
	To      string      `json:"to"`
	Days    int         `json:"days"`
	Gaps    []gapJSON   `json:"gaps"`
	Busiest busiestJSON `json:"busiest"`
	Skipped int         `json:"skipped"`
}

// newGapsCmd builds the `gaps` subcommand.
func newGapsCmd() *cobra.Command {
	var (
		asJSON bool
		quiet  bool
		days   int
		top    int
		tz     string
	)

	cmd := &cobra.Command{
		Use:   "gaps [crontab-file]",
		Short: "Find the longest quiet windows where nothing is scheduled to fire",
		Long: "Read a crontab (a file path, or stdin when omitted or '-'), merge the\n" +
			"fire times of every job over the next window, and report the longest\n" +
			"stretches of time where NOTHING fires — the safe slots for a heavy\n" +
			"backup, a deploy freeze, or a maintenance reboot.\n\n" +
			"It also reports the single busiest minute in the window and how many\n" +
			"jobs pile onto it (see `goblin stagger` to spread them). Unparseable\n" +
			"and never-firing lines are ignored for the math and counted as\n" +
			"skipped. A job that fires every minute leaves no gaps.",
		Example: "  goblin gaps crontab.txt                 # top 5 quiet windows over 7 days\n" +
			"  crontab -l | goblin gaps -              # analyze your live crontab\n" +
			"  goblin gaps --days 14 --top 10 c.txt    # wider window, more windows\n" +
			"  goblin gaps --tz America/New_York c.txt # in a specific timezone\n" +
			"  goblin gaps --json crontab.txt          # machine-readable report",
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			path := ""
			if len(args) == 1 {
				path = args[0]
			}

			loc, err := loadLocation(tz)
			if err != nil {
				if !quiet {
					fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line(tz))
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "error: unknown timezone %q: %v\n", tz, err)
				return err
			}

			src, label, err := readCrontabAll(cmd, path)
			if err != nil {
				if !quiet {
					fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line(path))
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", err)
				return err
			}

			rep, err := gaps.Analyze(src, time.Now(), days, top, loc)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "error: reading crontab: %v\n", err)
				return err
			}

			if asJSON {
				return writeGapsJSON(cmd, label, rep, days)
			}
			writeGapsHuman(cmd, label, rep, quiet)
			return nil
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "emit a stable machine-readable JSON report")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "silence the goblin's grumbling (stderr persona)")
	cmd.Flags().IntVar(&days, "days", gaps.DefaultDays, "look-ahead window in days")
	cmd.Flags().IntVar(&top, "top", gaps.DefaultTop, "how many quiet windows to report (longest first)")
	cmd.Flags().StringVar(&tz, "tz", "", "timezone for the analysis (IANA name, e.g. America/New_York; default: local)")

	return cmd
}

// writeGapsJSON renders the report as indented JSON on stdout. Times use RFC3339
// so agents get an unambiguous, timezone-carrying instant.
func writeGapsJSON(cmd *cobra.Command, src string, rep gaps.Report, days int) error {
	gs := make([]gapJSON, len(rep.Gaps))
	for i, g := range rep.Gaps {
		gs[i] = gapJSON{
			Start:           g.Start.Format(time.RFC3339),
			End:             g.End.Format(time.RFC3339),
			DurationSeconds: int64(g.Duration / time.Second),
		}
	}
	payload := gapsReportJSON{
		Source:  src,
		From:    rep.From.Format(time.RFC3339),
		To:      rep.To.Format(time.RFC3339),
		Days:    days,
		Gaps:    gs,
		Busiest: busiestJSON{Time: rep.Busiest.Time.Format(time.RFC3339), Count: rep.Busiest.Count},
		Skipped: rep.Skipped,
	}
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(payload)
}

// writeGapsHuman renders the report for a terminal: persona grumble on stderr,
// the ranked quiet windows and the busiest minute on stdout.
func writeGapsHuman(cmd *cobra.Command, src string, rep gaps.Report, quiet bool) {
	out := cmd.OutOrStdout()

	if !quiet {
		fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line(src+"gaps"))
	}

	if len(rep.Gaps) == 0 {
		fmt.Fprintf(out, "No quiet windows in %s over the next %s — something fires every minute.\n",
			src, humanizeSpan(rep.Window))
	} else {
		fmt.Fprintf(out, "Quiet windows in %s (nothing fires), longest first:\n", src)
		for i, g := range rep.Gaps {
			fmt.Fprintf(out, "  %d. %s → %s   (%s)\n",
				i+1,
				g.Start.Format("Mon 15:04"),
				g.End.Format("Mon 15:04"),
				humanizeSpan(g.Duration))
		}
	}

	if rep.Busiest.Count > 0 {
		jobWord := "job"
		if rep.Busiest.Count != 1 {
			jobWord = "jobs"
		}
		fmt.Fprintf(out, "Busiest minute: %s (%d %s)  ·  see `goblin stagger` to spread them.\n",
			rep.Busiest.Time.Format("Mon 15:04"), rep.Busiest.Count, jobWord)
	}

	if rep.Skipped > 0 {
		lineWord := "line"
		if rep.Skipped != 1 {
			lineWord = "lines"
		}
		fmt.Fprintf(out, "(%d %s ignored — unparseable or never fires.)\n", rep.Skipped, lineWord)
	}
}

// humanizeSpan formats a whole-minute duration like "3h33m", "2h", or "45m".
// Gaps are always minute multiples, so seconds are never shown.
func humanizeSpan(d time.Duration) string {
	total := int64(d / time.Minute)
	if total <= 0 {
		return "0m"
	}
	days := total / (24 * 60)
	total %= 24 * 60
	hours := total / 60
	mins := total % 60

	parts := make([]string, 0, 3)
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%dd", days))
	}
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if mins > 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%dm", mins))
	}
	return strings.Join(parts, "")
}
