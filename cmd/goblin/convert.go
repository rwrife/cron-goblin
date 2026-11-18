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
	From             string   `json:"from"`        // source dialect name
	To               string   `json:"to"`          // target dialect name
	Source           string   `json:"source"`      // the original expression
	Result           string   `json:"result"`      // the converted expression, in the target dialect
	Cron             string   `json:"cron"`        // resolved preview schedule; empty for unresolved H
	English          string   `json:"english"`     // plain-English readback of the result
	NextRuns         []string `json:"next_runs"`   // ISO fire times (UTC for EventBridge, otherwise local)
	NeverFires       bool     `json:"never_fires"` // true when the result never fires
	Warnings         []string `json:"warnings,omitempty"`
	JobName          string   `json:"job_name,omitempty"`
	PreviewAvailable bool     `json:"preview_available"`
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
		jobName   string
	)

	cmd := &cobra.Command{
		Use:   "convert --from <dialect> <expression>",
		Short: "Convert cron dialects, including AWS EventBridge and Jenkins",
		Long: "Translate a cron schedule between dialects. Deterministic and fully\n" +
			"offline.\n\n" +
			"EventBridge (--from/--to eventbridge) uses 6 fields (min hour DOM\n" +
			"month DOW year), optionally wrapped in cron(). Exactly one day field\n" +
			"must be `?`; weekdays are 1-7 and AWS rules use UTC only. L/W/# and\n" +
			"restricted years have no standard-cron equivalent and are refused.\n\n" +
			"Jenkins (--from/--to jenkins) supports H, H/n, H(a-b), H(a-b)/n\n" +
			"and its @macros. --job-name must be the full job name to resolve H.\n" +
			"Resolution assumes one trigger line and default Jenkins macro hashing.\n" +
			"--to jenkins intentionally jitters the minute, changing fire times;\n" +
			"without --job-name an unresolved H has no preview (cron is empty in\n" +
			"JSON, preview_available=false, not evidence that it never fires).\n" +
			"Jenkins ANDs day fields rather than ORing them; unrepresentable\n" +
			"both-day restrictions are refused.\n\n" +
			"Supported source dialects (--from):\n" +
			"  eventbridge  AWS rule cron (6 fields, UTC-only).\n" +
			"  jenkins     numeric 5-field cron with H hashing and @macros.\n" +
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
			"  eventbridge  AWS rule cron (6 fields, UTC-only).\n" +
			"  jenkins     H-jittered minute field (intentional schedule change).\n" +
			"  cron    standard 5-field cron.\n" +
			"  quartz  6-field Quartz (seconds prepended, `?` in the unused day\n" +
			"          field, weekdays renumbered 1-7). Refuses the one shape Quartz\n" +
			"          can't express: a cron that pins both day-of-month and day-of-week.\n" +
			"  k8s     a Kubernetes CronJob schedule (5-field cron; add --k8s-macros\n" +
			"          to collapse canonical forms to @daily/@hourly/... ).\n\n" +
			"Except for explicit Jenkins jitter, only lossless conversions succeed.\n" +
			"Constructs the target cannot express\n" +
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
			// systemd output has no single obvious canonical form yet.
			if dstDialect == dialect.Systemd {
				err := fmt.Errorf("converting to %q is not supported yet (targets: cron, quartz, k8s, eventbridge, jenkins)", to)
				fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", err)
				return err
			}

			cronExpr, err := convertToCron(srcDialect, source, jobName)
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

			// Resolve intentional target jitter before computing its preview.
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

			var warnings []string
			location := time.Local
			if srcDialect == dialect.EventBridge || dstDialect == dialect.EventBridge {
				location = time.UTC
				warnings = append(warnings, "EventBridge rules are UTC-only: clock fields are retained, not timezone-converted. Exactly one day field must be `?`.")
			}
			if srcDialect == dialect.Jenkins && jobName != "" {
				warnings = append(warnings, fmt.Sprintf("Jenkins H resolved using the full job name %q; renaming the job changes its schedule.", jobName))
			}
			previewAvailable := true
			if dstDialect == dialect.Jenkins {
				warnings = append(warnings, "Jenkins jitter intentionally changes fire times; only the minute field is jittered, not the original exact schedule.")
				if strings.Contains(resultExpr, "H") {
					if strings.TrimSpace(jobName) == "" {
						previewAvailable = false
						warnings = append(warnings, "Pass --job-name to resolve H and preview the jittered schedule.")
					} else {
						resolved, resolveErr := dialect.FromJenkins(resultExpr, jobName)
						if resolveErr != nil {
							fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", resolveErr)
							return resolveErr
						}
						sched, err = parse.Parse(resolved)
						if err != nil {
							fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", err)
							return err
						}
						warnings = append(warnings, fmt.Sprintf("Jenkins H resolves to %q for job %q.", sched.Raw, jobName))
					}
				}
			}
			readback := "Jenkins chooses stable H values from the full job name; no concrete schedule preview is available."
			canonicalCron := ""
			var runs []time.Time
			if previewAvailable {
				canonicalCron = sched.Raw
				readback = explain.Explain(sched)
				runs = nextrun.NextN(sched, time.Now(), count, location)
			}
			never := previewAvailable && len(runs) == 0
			isoRuns := make([]string, len(runs))
			for i, t := range runs {
				isoRuns[i] = t.Format(time.RFC3339)
			}

			if asJSON {
				payload := convertJSON{
					From:             string(srcDialect),
					To:               string(dstDialect),
					Source:           source,
					Result:           resultExpr,
					Cron:             canonicalCron,
					English:          readback,
					NextRuns:         isoRuns,
					NeverFires:       never,
					Warnings:         warnings,
					JobName:          jobName,
					PreviewAvailable: previewAvailable,
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
			for _, warning := range warnings {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", warning)
			}
			if !previewAvailable {
				return nil
			}
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

	cmd.Flags().StringVar(&from, "from", "", "source dialect (cron, quartz, systemd, k8s, eventbridge, jenkins)")
	cmd.Flags().StringVar(&to, "to", "cron", "target dialect (cron, quartz, k8s, eventbridge, jenkins)")
	cmd.Flags().StringVar(&jobName, "job-name", "", "full Jenkins job name (including folders), used as the H hash seed")
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
func convertToCron(src dialect.Dialect, expr, jobName string) (string, error) {
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
	case dialect.EventBridge:
		return dialect.FromEventBridge(expr)
	case dialect.Jenkins:
		return dialect.FromJenkins(expr, jobName)
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
	case dialect.EventBridge:
		return dialect.ToEventBridge(cron5)
	case dialect.Jenkins:
		return dialect.ToJenkins(cron5)
	default:
		return "", fmt.Errorf("converting to %q is not supported", dst)
	}
}
