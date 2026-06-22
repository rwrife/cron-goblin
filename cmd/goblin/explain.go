// explain.go implements `goblin explain`: parse a cron expression, describe it
// in plain English, and (as a placeholder until the M3 next-run engine lands)
// note where the upcoming fire times will go.
package main

import (
	"encoding/json"
	"fmt"

	"github.com/rwrife/cron-goblin/internal/explain"
	"github.com/rwrife/cron-goblin/internal/goblin"
	"github.com/rwrife/cron-goblin/internal/parse"
	"github.com/spf13/cobra"
)

// explainJSON is the machine-readable shape emitted by `explain --json`. It is
// intentionally small and stable so agents/scripts can depend on it; the
// next-run engine (M3) will populate NextRuns with real ISO timestamps.
type explainJSON struct {
	Expression  string     `json:"expression"`
	English     string     `json:"english"`
	Fields      fieldsJSON `json:"fields"`
	NextRuns    []string   `json:"next_runs"`
	NextRunNote string     `json:"next_runs_note,omitempty"`
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
			"Until the next-run engine lands (M3), the upcoming fire times are\n" +
			"reported as a placeholder.",
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
			const note = "next-run preview arrives in M3 (`goblin next`)"

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
					NextRuns:    []string{},
					NextRunNote: note,
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
			fmt.Fprintf(cmd.OutOrStdout(), "\nNext %d runs: %s\n", count, note)
			return nil
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "emit a machine-readable JSON summary")
	cmd.Flags().IntVarP(&count, "count", "n", 5, "how many upcoming runs to preview (placeholder until M3)")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "silence the goblin's grumbling (stderr persona)")

	return cmd
}
