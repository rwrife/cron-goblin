// convert.go implements `goblin convert`: translate a cron schedule from another
// dialect into standard 5-field cron (the only thing the rest of the tool, and
// most Unix crontabs, actually speak).
//
// The first supported source dialect is Quartz (the Java scheduler used by
// Spring, Elasticsearch, and friends), whose 6/7-field expressions,  `?` marker,
// and 1-7 weekday numbering trip people up constantly:
//
//	goblin convert --from quartz "0 0 9 ? * MON-FRI"   -> 0 9 * * MON-FRI
//	goblin convert --from quartz "0 30 2 * * ?"        -> 30 2 * * *
//
// When a Quartz construct genuinely has no standard-cron equivalent (a seconds
// value, a specific year, or the `L`/`W`/`#` specials), the goblin refuses with
// a specific error instead of silently emitting a wrong schedule.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rwrife/cron-goblin/internal/dialect"
	"github.com/rwrife/cron-goblin/internal/explain"
	"github.com/rwrife/cron-goblin/internal/goblin"
	"github.com/rwrife/cron-goblin/internal/nextrun"
	"github.com/rwrife/cron-goblin/internal/parse"
	"github.com/spf13/cobra"
)

// convertJSON is the machine-readable shape emitted by `convert --json`. It is
// deliberately small and stable: feed in a source expression + dialect, get back
// the standard-cron line (plus an English sanity-check and next runs) without
// scraping prose. On a lossy/impossible conversion the command errors out
// instead, so a successful JSON object always carries a usable Cron string.
type convertJSON struct {
	From       string   `json:"from"`        // source dialect name
	To         string   `json:"to"`          // target dialect name (always "cron" today)
	Source     string   `json:"source"`      // the original expression
	Cron       string   `json:"cron"`        // the converted 5-field cron
	English    string   `json:"english"`     // plain-English readback of the result
	NextRuns   []string `json:"next_runs"`   // ISO fire times (local TZ)
	NeverFires bool     `json:"never_fires"` // true when the result never fires
}

// newConvertCmd builds the `convert` subcommand.
func newConvertCmd() *cobra.Command {
	var (
		from   string
		to     string
		asJSON bool
		count  int
		quiet  bool
	)

	cmd := &cobra.Command{
		Use:   "convert --from <dialect> <expression>",
		Short: "Convert a cron schedule from another dialect into standard cron",
		Long: "Translate a cron schedule written in another dialect into a standard\n" +
			"5-field cron expression. Deterministic and fully offline.\n\n" +
			"Supported source dialect: quartz (6/7-field Java Quartz, with `?`,\n" +
			"1-7 SUN-SAT weekdays, and an optional year). The target is standard\n" +
			"cron (`--to cron`, the default); k8s CronJob schedules already are\n" +
			"standard cron.\n\n" +
			"Only lossless conversions succeed. Quartz features that standard cron\n" +
			"cannot express — sub-minute (seconds) precision, a specific year, and\n" +
			"the `L` (last), `W` (nearest weekday), and `#` (nth weekday) specials —\n" +
			"are refused with a specific error rather than silently mistranslated.\n\n" +
			"Pass --json for a machine-readable result an agent can consume.",
		Example: "  goblin convert --from quartz \"0 0 9 ? * MON-FRI\"\n" +
			"  goblin convert --from quartz \"0 30 2 * * ?\"\n" +
			"  goblin convert --from quartz --json \"0 0 12 1/2 * ?\"",
		// Accept the expression as one quoted arg, or several bare words we join,
		// so `goblin convert --from quartz 0 0 9 ? * MON-FRI` works unquoted too.
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			source := strings.Join(args, " ")

			srcDialect, err := dialect.ParseDialect(from)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", err)
				return err
			}
			dstDialect, err := dialect.ParseDialect(to)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", err)
				return err
			}
			// The only target this slice can produce is standard 5-field cron.
			// k8s is standard cron, so treat it as an alias of the cron target.
			if dstDialect != dialect.Cron && dstDialect != dialect.K8s {
				err := fmt.Errorf("converting to %q is not supported yet (target must be cron)", to)
				fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", err)
				return err
			}

			cronExpr, err := convertToCron(srcDialect, source)
			if err != nil {
				// The goblin grumbles, then we print the concrete diagnostic. On a
				// lossy refusal, add a one-line hint about *why* it can't be helped.
				if !quiet {
					fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line(source))
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", err)
				var ce *dialect.ConvertError
				if errors.As(err, &ce) && ce.Lossy {
					fmt.Fprintln(cmd.ErrOrStderr(),
						"hint: standard 5-field cron can't carry this; pin it in the scheduler that speaks the source dialect.")
				}
				return err
			}

			// Round-trip through the trusted parser so the readback/preview come
			// from the same normalized Schedule everything else uses, and so we
			// validate ranges the dialect layer intentionally left to parse.
			sched, perr := parse.Parse(cronExpr)
			if perr != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "error: converted to invalid cron %q: %v\n", cronExpr, perr)
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
				payload := convertJSON{
					From:       string(srcDialect),
					To:         string(dialect.Cron),
					Source:     source,
					Cron:       sched.Raw,
					English:    readback,
					NextRuns:   isoRuns,
					NeverFires: never,
				}
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(payload)
			}

			// Human output: the converted cron line is the star and lands on the
			// first stdout line alone, so it's trivially pipeable; supporting
			// detail follows as comments, persona goes to stderr.
			if !quiet {
				fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line(source))
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

	cmd.Flags().StringVar(&from, "from", "", "source dialect to convert from (quartz)")
	cmd.Flags().StringVar(&to, "to", "cron", "target dialect (cron)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit a machine-readable JSON result")
	cmd.Flags().IntVarP(&count, "count", "n", 1, "how many upcoming runs to compute for the preview")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "silence the goblin's grumbling (stderr persona)")
	_ = cmd.MarkFlagRequired("from")

	return cmd
}

// convertToCron dispatches to the right dialect converter. It exists as a small
// seam so additional source dialects (systemd, etc.) slot in without touching
// the command wiring, and so the "not yet supported" story is one clear place.
func convertToCron(src dialect.Dialect, expr string) (string, error) {
	switch src {
	case dialect.Quartz:
		return dialect.FromQuartz(expr)
	case dialect.Cron, dialect.K8s:
		// Already standard cron (k8s schedules are 5-field cron): validate shape
		// by parsing, then hand back the normalized form.
		sched, err := parse.Parse(expr)
		if err != nil {
			return "", err
		}
		return sched.Raw, nil
	case dialect.Systemd:
		return "", fmt.Errorf("converting from systemd OnCalendar is not supported yet")
	default:
		return "", fmt.Errorf("converting from %q is not supported", src)
	}
}
