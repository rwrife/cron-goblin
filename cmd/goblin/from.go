// from.go implements `goblin from`: the inverse of `explain`. Give it a plain-
// English schedule phrase and it prints the equivalent 5-field cron expression
// (deterministic, offline — see internal/english), plus a short confirmation of
// what that cron means and a preview of the next few fire times.
//
// This is the M6 headline command:
//
//	goblin from "every 15 minutes"      -> */15 * * * *
//	goblin from "every weekday at 6:30pm" -> 30 18 * * 1-5
package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/rwrife/cron-goblin/internal/english"
	"github.com/rwrife/cron-goblin/internal/explain"
	"github.com/rwrife/cron-goblin/internal/goblin"
	"github.com/rwrife/cron-goblin/internal/nextrun"
	"github.com/rwrife/cron-goblin/internal/parse"
	"github.com/spf13/cobra"
)

// fromJSON is the machine-readable shape emitted by `from --json`. It is kept
// small and stable for agents/scripts: feed in a phrase, get back a cron line
// (and a sanity-check English readback + next runs) without parsing prose.
type fromJSON struct {
	Phrase     string   `json:"phrase"`
	Cron       string   `json:"cron"`
	English    string   `json:"english"`
	NextRuns   []string `json:"next_runs"`
	NeverFires bool     `json:"never_fires"`
}

// newFromCmd builds the `from` subcommand.
func newFromCmd() *cobra.Command {
	var (
		asJSON bool
		count  int
		quiet  bool
	)

	cmd := &cobra.Command{
		Use:   "from <english-phrase>",
		Short: "Turn a plain-English schedule into a cron expression",
		Long: "Translate a plain-English schedule phrase into a standard 5-field\n" +
			"cron expression. Deterministic and fully offline — no LLM, no network.\n\n" +
			"Understood phrases cover the common cases: \"every 15 minutes\",\n" +
			"\"every day at 9am\", \"every weekday at 6:30pm\", \"weekends at noon\",\n" +
			"\"every monday at 8am\", \"first of the month at 9am\". Anything outside\n" +
			"the grammar is rejected (with a grumble) rather than guessed at.\n\n" +
			"Pass --json for a machine-readable result an agent can consume.",
		Example: "  goblin from \"every 15 minutes\"\n" +
			"  goblin from \"every weekday at 6:30pm\"\n" +
			"  goblin from --json \"daily at 9am\"",
		// Accept the phrase as one quoted arg, or several bare words that we join
		// (so `goblin from every 15 minutes` works without quotes too).
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			phrase := strings.Join(args, " ")

			expr, err := english.Parse(phrase)
			if err != nil {
				if !quiet {
					fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line(phrase))
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", err)
				return err
			}

			// Round-trip through the trusted parser so the preview/readback are
			// computed from the same normalized Schedule everything else uses. This
			// should never fail (english only emits valid cron), but guard anyway.
			sched, perr := parse.Parse(expr)
			if perr != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "error: produced invalid cron %q: %v\n", expr, perr)
				return perr
			}

			readback := explain.Explain(sched)
			runs := nextrun.NextN(sched, time.Now(), count, time.Local)
			never := len(runs) == 0
			isoRuns := make([]string, len(runs))
			for i, t := range runs {
				isoRuns[i] = t.Format(time.RFC3339)
			}

			if asJSON {
				payload := fromJSON{
					Phrase:     phrase,
					Cron:       sched.Raw,
					English:    readback,
					NextRuns:   isoRuns,
					NeverFires: never,
				}
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(payload)
			}

			// Human output: the cron line is the star and goes to stdout alone on
			// the first line, so it's trivially pipeable (`goblin from "..." | crontab`
			// style usage). Everything else is supporting detail.
			if !quiet {
				fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line(phrase))
			}
			fmt.Fprintln(cmd.OutOrStdout(), sched.Raw)
			fmt.Fprintf(cmd.OutOrStdout(), "# %s\n", readback)
			if never {
				fmt.Fprintln(cmd.OutOrStdout(),
					"# (heads up: this schedule never actually fires)")
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "# next: %s\n", runs[0].Format(time.RFC3339))
			return nil
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "emit a machine-readable JSON result")
	cmd.Flags().IntVarP(&count, "count", "n", 1, "how many upcoming runs to compute for the preview")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "silence the goblin's grumbling (stderr persona)")

	return cmd
}
