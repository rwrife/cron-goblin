package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/rwrife/cron-goblin/internal/coverage"
	"github.com/rwrife/cron-goblin/internal/goblin"
	"github.com/spf13/cobra"
)

func newCoverageCmd() *cobra.Command {
	var want, tz, window string
	var asJSON, quiet bool
	cmd := &cobra.Command{
		Use:   "coverage [crontab-file] --want <english-or-cron>",
		Short: "Check whether a crontab covers every desired fire time",
		Long: "Compare the union of all jobs with a desired English or cron schedule.\n" +
			"Each desired minute needs at least one job firing at that exact instant.\n" +
			"Missing instants are gaps; multiple jobs at a desired instant are\n" +
			"redundancies. Other fire times are ignored. Both findings can coexist.\n\n" +
			"The window is strictly after now through now + --window (default 7d).\n" +
			"Read a file, or stdin when omitted or '-'. Malformed jobs are errors.\n" +
			"Exit non-zero for gaps or input errors, but not for redundancies alone.",
		Example: "  goblin coverage crontab.txt --want \"every 15 minutes\" --tz UTC\n" +
			"  goblin coverage - --want \"0 9 * * 1-5\" --window 30d --json",
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			fail := func(err error) error {
				fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", err)
				return err
			}
			win, err := parseWindow(window)
			if err != nil {
				return fail(err)
			}
			loc, err := loadLocation(tz)
			if err != nil {
				return fail(fmt.Errorf("unknown timezone %q: %w", tz, err))
			}
			path := ""
			if len(args) > 0 {
				path = args[0]
			}
			src, label, err := readCrontabAll(cmd, path)
			if err != nil {
				return fail(err)
			}
			rep, err := coverage.Analyze(src, want, time.Now(), win, loc)
			if err != nil {
				return fail(err)
			}
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if err := enc.Encode(rep); err != nil {
					return fail(err)
				}
			} else {
				if !quiet {
					fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line(label+"coverage"))
				}
				out := cmd.OutOrStdout()
				fmt.Fprintf(out, "%s: %s (%d/%d desired fires covered)\n", label, rep.Status, rep.Covered, rep.Expected)
				fmt.Fprintf(out, "Window: %s through %s (%s)\n",
					rep.From.Format(time.RFC3339), rep.To.Format(time.RFC3339), rep.Timezone)
				if rep.Expected == 0 {
					fmt.Fprintln(out, "No desired fires in this window; widen --window to check a sparser cadence.")
				}
				for _, gap := range rep.Gaps {
					fmt.Fprintf(out, "  Gap: no job fires at %s\n", gap.Time.Format(time.RFC3339))
				}
				for _, redundant := range rep.Redundancies {
					fmt.Fprintf(out, "  Redundancy: %d jobs fire at %s (lines %v)\n",
						redundant.Count, redundant.Time.Format(time.RFC3339), redundant.Lines)
				}
			}
			if len(rep.Gaps) > 0 {
				return fail(fmt.Errorf("%d desired fire times uncovered", len(rep.Gaps)))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&want, "want", "", "desired cadence as supported English or a five-field cron expression (required)")
	cmd.Flags().StringVar(&tz, "tz", "", "timezone for analysis (IANA name; default: local)")
	cmd.Flags().StringVar(&window, "window", "7d", "analysis duration (e.g. 7d, 30d, 48h)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit a structured report with gaps and redundancies")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "silence the goblin's grumbling (stderr persona)")
	_ = cmd.MarkFlagRequired("want")
	return cmd
}
