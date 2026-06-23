// next.go implements `goblin next`: parse a cron expression and list the next N
// times it will actually fire, in a chosen timezone. This is the user-facing
// surface of the M3 nextrun engine.
package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/rwrife/cron-goblin/internal/explain"
	"github.com/rwrife/cron-goblin/internal/goblin"
	"github.com/rwrife/cron-goblin/internal/nextrun"
	"github.com/rwrife/cron-goblin/internal/parse"
	"github.com/spf13/cobra"
)

// nextJSON is the machine-readable shape emitted by `next --json`. Stable and
// small on purpose so agents/scripts can depend on it.
type nextJSON struct {
	Expression string   `json:"expression"`
	English    string   `json:"english"`
	Timezone   string   `json:"timezone"`
	Count      int      `json:"count"`
	NextRuns   []string `json:"next_runs"`
	NeverFires bool     `json:"never_fires"`
}

// loadLocation resolves a timezone flag value to a *time.Location. An empty
// value means local time. "UTC" and IANA names (e.g. "America/New_York") are
// accepted; anything time.LoadLocation understands works.
func loadLocation(tz string) (*time.Location, error) {
	if tz == "" {
		return time.Local, nil
	}
	return time.LoadLocation(tz)
}

// isoStable formats a fire time as a timezone-qualified RFC3339 string. We keep
// the numeric offset (not just "Z") so the output is unambiguous across zones.
func isoStable(t time.Time) string {
	return t.Format(time.RFC3339)
}

// newNextCmd builds the `next` subcommand.
func newNextCmd() *cobra.Command {
	var (
		asJSON bool
		count  int
		tz     string
		quiet  bool
	)

	cmd := &cobra.Command{
		Use:   "next <cron-expression>",
		Short: "List the next N times a cron expression fires",
		Long: "Parse a standard 5-field cron expression and print the next N times\n" +
			"it will fire, in your local timezone (or --tz). Fire times honor\n" +
			"cron's day-of-month/day-of-week OR-rule and skip impossible dates.\n\n" +
			"Expressions that can never fire (for example `0 0 30 2 *`, February\n" +
			"30th) are reported as such instead of hanging. Pass --json for a\n" +
			"machine-readable summary.",
		Example: "  goblin next \"*/15 * * * *\" -n 20\n" +
			"  goblin next --tz America/New_York \"0 9 * * 1-5\"\n" +
			"  goblin next --json \"0 0 13 * 5\"",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			expr := args[0]

			if count <= 0 {
				return fmt.Errorf("count (-n) must be positive, got %d", count)
			}

			loc, err := loadLocation(tz)
			if err != nil {
				if !quiet {
					fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line(tz))
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "error: unknown timezone %q: %v\n", tz, err)
				return err
			}

			sched, err := parse.Parse(expr)
			if err != nil {
				if !quiet {
					fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line(expr))
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", err)
				return err
			}

			english := explain.Explain(sched)
			now := time.Now()
			runs := nextrun.NextN(sched, now, count, loc)
			never := len(runs) == 0

			isoRuns := make([]string, len(runs))
			for i, t := range runs {
				isoRuns[i] = isoStable(t)
			}

			if asJSON {
				payload := nextJSON{
					Expression: sched.Raw,
					English:    english,
					Timezone:   loc.String(),
					Count:      count,
					NextRuns:   isoRuns,
					NeverFires: never,
				}
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(payload)
			}

			// Human output: grumble on stderr, facts on stdout.
			if !quiet {
				fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line(expr))
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s\n%s\n\n", sched.Raw, english)
			if never {
				fmt.Fprintf(cmd.OutOrStdout(),
					"This schedule never fires — no matching date exists (checked %d years ahead).\n",
					nextrun.DefaultHorizonYears)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Next %d run(s) in %s:\n", len(runs), loc.String())
			for _, t := range runs {
				fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", isoStable(t))
			}
			return nil
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "emit a machine-readable JSON summary")
	cmd.Flags().IntVarP(&count, "count", "n", 5, "how many upcoming runs to list")
	cmd.Flags().StringVar(&tz, "tz", "", "timezone for fire times (IANA name, e.g. America/New_York; default: local)")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "silence the goblin's grumbling (stderr persona)")

	return cmd
}
