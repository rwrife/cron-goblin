// narrate.go implements `goblin narrate`: emit a warm, prose one-liner
// describing a cron schedule — or a schedule *change* — suitable for
// changelogs, release notes, and PR descriptions.
//
// Where `explain` is terse and structured, `narrate` is a full human sentence
// you can paste straight into a CHANGELOG. Given --from/--to it describes the
// change (cadence delta + next-run shift), pairing naturally with `diff`.
//
// It is deterministic and offline: it reuses the shared explain grammar and the
// M3 nextrun engine, never an LLM.
package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/rwrife/cron-goblin/internal/goblin"
	"github.com/rwrife/cron-goblin/internal/narrate"
	"github.com/rwrife/cron-goblin/internal/parse"
	"github.com/spf13/cobra"
)

// narrateJSON is the machine-readable shape emitted by `narrate --json`. It is
// intentionally small and stable so agents/scripts can depend on it. In change
// mode both From and To are populated; in single mode only Expression is.
type narrateJSON struct {
	Sentence   string `json:"sentence"`
	Expression string `json:"expression,omitempty"`
	From       string `json:"from,omitempty"`
	To         string `json:"to,omitempty"`
	Change     bool   `json:"change"`
}

// newNarrateCmd builds the `narrate` subcommand.
func newNarrateCmd() *cobra.Command {
	var (
		asJSON   bool
		fromExpr string
		toExpr   string
		tz       string
		quiet    bool
	)

	cmd := &cobra.Command{
		Use:   "narrate [cron-expression]",
		Short: "Describe a schedule (or a schedule change) in changelog-ready prose",
		Long: "Emit a single warm, plain-English sentence describing when a cron\n" +
			"schedule fires — phrased for changelogs, release notes, and PR\n" +
			"descriptions. Unlike `explain` (terse and structured), `narrate` is\n" +
			"human-readable prose you can paste straight into a CHANGELOG.\n\n" +
			"Pass a single expression to narrate it, or --from/--to to narrate the\n" +
			"*change* between two schedules (cadence delta + next-run shift); this\n" +
			"pairs naturally with `goblin diff`. --tz picks the zone the next-run\n" +
			"shift is evaluated in. --json returns {sentence, ...} for agents.\n\n" +
			"Deterministic and offline: no LLM, no network.",
		Example: "  goblin narrate \"30 18 * * 1-5\"\n" +
			"  goblin narrate --from \"0 * * * *\" --to \"30 18 * * 1-5\"\n" +
			"  goblin narrate --json \"0 0 * * 0\"",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			changeMode := fromExpr != "" || toExpr != ""

			if changeMode {
				if fromExpr == "" || toExpr == "" {
					err := fmt.Errorf("--from and --to must be used together")
					fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", err)
					return err
				}
				if len(args) > 0 {
					err := fmt.Errorf("pass either a single expression OR --from/--to, not both")
					fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", err)
					return err
				}
			} else if len(args) != 1 {
				err := fmt.Errorf("narrate needs one cron expression (or --from/--to)")
				fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", err)
				return err
			}

			loc, err := loadLocation(tz)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "error: unknown timezone %q: %v\n", tz, err)
				return err
			}

			var (
				sentence string
				payload  narrateJSON
			)

			if changeMode {
				oldS, err := parseOrGrumble(cmd, fromExpr, quiet)
				if err != nil {
					return err
				}
				newS, err := parseOrGrumble(cmd, toExpr, quiet)
				if err != nil {
					return err
				}
				sentence = narrate.NarrateChange(oldS, newS, time.Now(), loc)
				payload = narrateJSON{
					Sentence: sentence,
					From:     oldS.Raw,
					To:       newS.Raw,
					Change:   true,
				}
			} else {
				sched, err := parseOrGrumble(cmd, args[0], quiet)
				if err != nil {
					return err
				}
				sentence = narrate.Narrate(sched)
				payload = narrateJSON{
					Sentence:   sentence,
					Expression: sched.Raw,
					Change:     false,
				}
			}

			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(payload)
			}

			fmt.Fprintln(cmd.OutOrStdout(), sentence)
			return nil
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "emit a machine-readable JSON summary")
	cmd.Flags().StringVar(&fromExpr, "from", "", "old expression (change mode; use with --to)")
	cmd.Flags().StringVar(&toExpr, "to", "", "new expression (change mode; use with --from)")
	cmd.Flags().StringVar(&tz, "tz", "", "timezone for the next-run shift (IANA name; default: local)")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "silence the goblin's grumbling (stderr persona)")

	return cmd
}

// parseOrGrumble parses expr, letting the goblin grumble on stderr and printing
// a concrete diagnostic when parsing fails.
func parseOrGrumble(cmd *cobra.Command, expr string, quiet bool) (parse.Schedule, error) {
	sched, err := parse.Parse(expr)
	if err != nil {
		if !quiet {
			fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line(expr))
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", err)
		return parse.Schedule{}, err
	}
	return sched, nil
}
