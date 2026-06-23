// explain.go implements `goblin explain`: parse a cron expression, describe it
// in plain English, and preview the upcoming fire times (via the M3 nextrun
// engine).
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

// explainJSON is the machine-readable shape emitted by `explain --json`. It is
// intentionally small and stable so agents/scripts can depend on it. NextRuns
// holds real ISO timestamps from the M3 nextrun engine (local timezone); it is
// empty for expressions that never fire (e.g. February 30th).
type explainJSON struct {
	Expression string     `json:"expression"`
	English    string     `json:"english"`
	Fields     fieldsJSON `json:"fields"`
	NextRuns   []string   `json:"next_runs"`
	NeverFires bool       `json:"never_fires"`
}

// fieldsJSON exposes the normalized per-field value sets.
type fieldsJSON struct {
	Minute     []int `json:"minute"`
	Hour       []int `json:"hour"`
	DayOfMonth []int `json:"day_of_month"`
	Month      []int `json:"month"`
	DayOfWeek  []int `json:"day_of_week"`
}

// newExplainCmd builds the `explain` subcommand. version is unused today but is
// passed in for symmetry with the root constructor and future --version-aware
// behavior.
func newExplainCmd() *cobra.Command {
	var (
		asJSON bool
		count  int
		quiet  bool
	)

	cmd := &cobra.Command{
		Use:   "explain <cron-expression>",
		Short: "Translate a cron expression into plain English",
		Long: "Parse a standard 5-field cron expression and describe, in plain\n" +
			"English, when it fires. Pass --json for a machine-readable summary.\n\n" +
			"A preview of the next few fire times (local timezone) is included;\n" +
			"use `goblin next` for more control (count, timezone).",
		Example: "  goblin explain \"*/15 9-17 * * 1-5\"\n" +
			"  goblin explain --json \"0 0 * * 0\"",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			expr := args[0]

			sched, err := parse.Parse(expr)
			if err != nil {
				// Persona is allowed to grumble about bad input on stderr, then we
				// print the concrete diagnostic ourselves (errors are silenced at the
				// command level so cobra doesn't also dump usage).
				if !quiet {
					fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line(expr))
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", err)
				return err
			}

			english := explain.Explain(sched)
			runs := nextrun.NextN(sched, time.Now(), count, time.Local)
			never := len(runs) == 0
			isoRuns := make([]string, len(runs))
			for i, t := range runs {
				isoRuns[i] = t.Format(time.RFC3339)
			}

			if asJSON {
				payload := explainJSON{
					Expression: sched.Raw,
					English:    english,
					Fields: fieldsJSON{
						Minute:     sched.Minute.Values,
						Hour:       sched.Hour.Values,
						DayOfMonth: sched.DOM.Values,
						Month:      sched.Month.Values,
						DayOfWeek:  sched.DOW.Values,
					},
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
			fmt.Fprintf(cmd.OutOrStdout(), "%s\n%s\n", sched.Raw, english)
			if never {
				fmt.Fprintf(cmd.OutOrStdout(),
					"\nThis schedule never fires — no matching date exists.\n")
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "\nNext %d run(s):\n", len(runs))
			for _, t := range runs {
				fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", t.Format(time.RFC3339))
			}
			return nil
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "emit a machine-readable JSON summary")
	cmd.Flags().IntVarP(&count, "count", "n", 5, "how many upcoming runs to preview")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "silence the goblin's grumbling (stderr persona)")

	return cmd
}
