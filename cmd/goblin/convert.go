// convert.go implements `goblin convert`: translate a cron schedule from another
// dialect into standard 5-field cron (the only thing the rest of the tool, and
// most Unix crontabs, actually speak).
//
// The supported source dialects are Quartz (the Java scheduler used by Spring,
// Elasticsearch, and friends) and systemd's OnCalendar timer syntax — both trip
// people up constantly:
//
//	goblin convert --from quartz  "0 0 9 ? * MON-FRI"  -> 0 9 * * MON-FRI
//	goblin convert --from quartz  "0 30 2 * * ?"        -> 30 2 * * *
//	goblin convert --from systemd "Mon..Fri 09:00"     -> 0 9 * * MON-FRI
//	goblin convert --from systemd weekly               -> 0 0 * * MON
//
// When a construct genuinely has no standard-cron equivalent (a seconds value,
// a specific year, Quartz's `L`/`W`/`#`, or systemd's `~`), the goblin refuses
// with a specific error instead of silently emitting a wrong schedule.
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
// the converted line (plus an English sanity-check and next runs) without
// scraping prose. On a lossy/impossible conversion the command errors out
// instead, so a successful JSON object always carries a usable Result string.
type convertJSON struct {
	From       string   `json:"from"`        // source dialect name
	To         string   `json:"to"`          // target dialect name (cron, quartz, or k8s)
	Source     string   `json:"source"`      // the original expression
	Result     string   `json:"result"`      // the converted expression, in the target dialect
	Cron       string   `json:"cron"`        // the canonical 5-field cron the result is built from
	English    string   `json:"english"`     // plain-English readback of the result
	NextRuns   []string `json:"next_runs"`   // ISO fire times (local TZ)
	NeverFires bool     `json:"never_fires"` // true when the result never fires
}

// newConvertCmd builds the `convert` subcommand.
func newConvertCmd() *cobra.Command {
	var (
		from      string
		to        string
		asJSON    bool
		count     int
		quiet     bool
		k8sMacros bool
	)

	cmd := &cobra.Command{
		Use:   "convert --from <dialect> <expression>",
		Short: "Convert a cron schedule from another dialect into standard cron",
		Long: "Translate a cron schedule between dialects. Deterministic and fully\n" +
			"offline.\n\n" +
			"Supported source dialects (--from):\n" +
			"  quartz  6/7-field Java Quartz, with `?`, 1-7 SUN-SAT weekdays,\n" +
			"          and an optional year.\n" +
			"  systemd systemd.timer OnCalendar (DayOfWeek Y-M-D H:M:S), plus the\n" +
			"          named shorthands daily/weekly/monthly/quarterly/yearly.\n" +
			"  k8s     Kubernetes CronJob schedules: 5-field cron plus the robfig\n" +
			"          `@`-macros (@daily, @hourly, @weekly, ...). Validates that a\n" +
			"          manifest schedule is one the apiserver will accept, refusing\n" +
			"          vixie-only @reboot and pasted-in Quartz specials.\n" +
			"  cron    standard 5-field cron (the default; useful with --to quartz).\n\n" +
			"Supported target dialects (--to, default cron):\n" +
			"  cron    standard 5-field cron.\n" +
			"  quartz  6-field Quartz (seconds prepended, `?` in the unused day\n" +
			"          field, weekdays renumbered 1-7). Refuses the one shape Quartz\n" +
			"          can't express: a cron that pins both day-of-month and day-of-week.\n" +
			"  k8s     a Kubernetes CronJob schedule (5-field cron; add --k8s-macros\n" +
			"          to collapse canonical forms to @daily/@hourly/... ).\n\n" +
			"Only lossless conversions succeed. Constructs the target cannot express\n" +
			"— sub-minute (seconds) precision, a specific year, Quartz's `L`/`W`/`#`,\n" +
			"systemd's `~`, or a both-day-fields cron into Quartz — are refused with a\n" +
			"specific error rather than silently mistranslated.\n\n" +
			"Pass --json for a machine-readable result an agent can consume.",
		Example: "  goblin convert --from quartz \"0 0 9 ? * MON-FRI\"\n" +
			"  goblin convert --from quartz \"0 30 2 * * ?\"\n" +
			"  goblin convert --from cron --to quartz \"0 9 * * MON-FRI\"\n" +
			"  goblin convert --from systemd \"Mon..Fri 09:00\"\n" +
			"  goblin convert --from systemd \"*-*-01 00:00\"\n" +
			"  goblin convert --from k8s \"@daily\"\n" +
			"  goblin convert --from cron --to k8s --k8s-macros \"0 0 * * *\"\n" +
			"  goblin convert --from systemd --json weekly",
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
			// Supported targets: standard cron, Quartz, and k8s. systemd output is
			// not implemented yet (its richer grammar has no single obvious form).
			if dstDialect == dialect.Systemd {
				err := fmt.Errorf("converting to %q is not supported yet (targets: cron, quartz, k8s)", to)
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

			// Serialize the normalized cron into the requested target dialect. The
			// English readback and next-run preview above come from the canonical
			// Schedule, so they stay correct regardless of how the result is spelled.
			resultExpr, err := renderToDialect(dstDialect, sched.Raw, k8sMacros)
			if err != nil {
				if !quiet {
					fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line(source))
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", err)
				var ce *dialect.ConvertError
				if errors.As(err, &ce) && ce.Lossy {
					fmt.Fprintln(cmd.ErrOrStderr(),
						"hint: this schedule has no lossless form in the target dialect; keep it in the dialect that can express it.")
				}
				return err
			}

			if asJSON {
				payload := convertJSON{
					From:       string(srcDialect),
					To:         string(dstDialect),
					Source:     source,
					Result:     resultExpr,
					Cron:       sched.Raw,
					English:    readback,
					NextRuns:   isoRuns,
					NeverFires: never,
				}
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(payload)
			}

			// Human output: the converted line is the star and lands on the first
			// stdout line alone, so it's trivially pipeable; supporting detail follows
			// as comments, persona goes to stderr.
			if !quiet {
				fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line(source))
			}
			fmt.Fprintln(cmd.OutOrStdout(), resultExpr)
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

	cmd.Flags().StringVar(&from, "from", "", "source dialect to convert from (quartz, systemd, k8s)")
	cmd.Flags().StringVar(&to, "to", "cron", "target dialect (cron, quartz, k8s)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit a machine-readable JSON result")
	cmd.Flags().IntVarP(&count, "count", "n", 1, "how many upcoming runs to compute for the preview")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "silence the goblin's grumbling (stderr persona)")
	cmd.Flags().BoolVar(&k8sMacros, "k8s-macros", false, "with --to k8s, collapse canonical schedules to @daily/@hourly/... macros")
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
	case dialect.Cron:
		// Already standard cron: validate shape by parsing, then hand back the
		// normalized form.
		sched, err := parse.Parse(expr)
		if err != nil {
			return "", err
		}
		return sched.Raw, nil
	case dialect.K8s:
		// Kubernetes CronJob schedules are 5-field cron *plus* robfig/cron's `@`
		// macros (@daily, @hourly, ...), and they reject vixie-only `@reboot` and
		// Quartz specials. FromK8s expands the macros and refuses the rest with a
		// k8s-specific reason; the result is validated by parse.Parse downstream.
		return dialect.FromK8s(expr)
	case dialect.Systemd:
		return dialect.FromSystemd(expr)
	default:
		return "", fmt.Errorf("converting from %q is not supported", src)
	}
}

// renderToDialect serializes a normalized 5-field cron expression into the
// requested target dialect. It is the reverse-direction counterpart of
// convertToCron: the source is always canonical standard cron (produced and
// validated upstream), and this decides how the result is spelled.
//
// Cron is returned as-is. Quartz gains its seconds field and `?`/weekday
// renumbering via dialect.ToQuartz (which refuses the one both-day-fields shape
// Quartz cannot express). K8s is standard cron, optionally collapsed to an
// `@`-macro when the caller passed --k8s-macros.
func renderToDialect(dst dialect.Dialect, cron5 string, k8sMacros bool) (string, error) {
	switch dst {
	case dialect.Cron:
		return cron5, nil
	case dialect.Quartz:
		return dialect.ToQuartz(cron5)
	case dialect.K8s:
		return dialect.ToK8s(cron5, k8sMacros)
	default:
		return "", fmt.Errorf("converting to %q is not supported", dst)
	}
}
