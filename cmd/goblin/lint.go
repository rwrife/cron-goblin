// lint.go implements `goblin lint`: read a crontab (file path or stdin), treat
// it like a program, and report problems — dead expressions, too-frequent
// jobs, and same-instant collisions. This is the user-facing surface of the M4
// internal/lint engine.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/rwrife/cron-goblin/internal/goblin"
	"github.com/rwrife/cron-goblin/internal/lint"
	"github.com/spf13/cobra"
)

// lintFindingJSON is the stable per-finding shape in `lint --json`.
type lintFindingJSON struct {
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Lines    []int  `json:"lines"`
}

// lintReportJSON is the machine-readable shape emitted by `lint --json`.
// Deliberately small and stable so agents/CI can depend on it. Counts mirror
// the human summary line; ok is true when nothing worse than info was found.
type lintReportJSON struct {
	Source   string            `json:"source"`
	Entries  int               `json:"entries"`
	Findings []lintFindingJSON `json:"findings"`
	Counts   lintCountsJSON    `json:"counts"`
	Worst    string            `json:"worst"`
	OK       bool              `json:"ok"`
}

// lintCountsJSON tallies findings by severity in the JSON report.
type lintCountsJSON struct {
	Info    int `json:"info"`
	Warning int `json:"warning"`
	Error   int `json:"error"`
}

// newLintCmd builds the `lint` subcommand.
func newLintCmd() *cobra.Command {
	var (
		asJSON  bool
		ci      bool
		ciLevel string
		quiet   bool
		tz      string
	)

	cmd := &cobra.Command{
		Use:   "lint [crontab-file]",
		Short: "Lint a crontab: dead expressions, too-frequent jobs, collisions, DST hazards",
		Long: "Read a crontab (a file path, or stdin when omitted or '-') and check it\n" +
			"like a linter checks code. Rules:\n\n" +
			"  • dead-expression — schedules that can never fire (error)\n" +
			"  • too-frequent    — every-minute / runaway cadences (warning)\n" +
			"  • collision       — jobs that fire at the same instant (warning)\n" +
			"  • dst-danger      — jobs landing in a daylight-saving gap/overlap\n" +
			"                      (needs --tz; warning when skipped, info when ambiguous)\n\n" +
			"Use --json for a stable machine-readable report, and --ci to exit\n" +
			"non-zero when any warning or error is found (handy in pipelines).",
		Example: "  goblin lint /etc/crontab\n" +
			"  crontab -l | goblin lint -\n" +
			"  goblin lint --json crontab.txt\n" +
			"  goblin lint --tz America/New_York crontab.txt   # flag DST hazards\n" +
			"  goblin lint --ci crontab.txt   # non-zero exit on warnings/errors",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := ""
			if len(args) == 1 {
				path = args[0]
			}

			// Project config (.goblinrc) supplies defaults *under* explicit
			// flags: it can preset the timezone, disable rules, and turn on CI
			// mode. --no-config (persistent) bypasses discovery entirely.
			cfg, cerr := loadConfig(cmd, quiet)
			if cerr != nil {
				return cerr
			}
			tz = resolveTZ(tz, cmd.Flags().Changed("tz"), cfg)
			if cfg.CIEnabled() {
				ci = true
			}

			// Setting --ci-level (or ci_level in config) implies CI gating: you
			// asked for a threshold, so you clearly want the gate on.
			if !cmd.Flags().Changed("ci-level") && cfg.Lint.CILevel != "" {
				ciLevel = cfg.Lint.CILevel
			}
			if cmd.Flags().Changed("ci-level") || cfg.Lint.CILevel != "" {
				ci = true
			}
			failLevel, err := parseCILevel(ciLevel)
			if err != nil {
				if !quiet {
					fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line(ciLevel))
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", err)
				return err
			}

			// Resolve the timezone up front so a bad --tz fails fast with the
			// same goblin grumble as the other zone-aware commands. An empty
			// --tz means "no DST analysis" (local would be ambiguous for a file
			// that may target another host), keeping default output unchanged.
			var loc *time.Location
			if tz != "" {
				l, err := loadLocation(tz)
				if err != nil {
					if !quiet {
						fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line(tz))
					}
					fmt.Fprintf(cmd.ErrOrStderr(), "error: unknown timezone %q: %v\n", tz, err)
					return err
				}
				loc = l
			}

			src, reader, closer, err := openCrontab(cmd, path)
			if err != nil {
				if !quiet {
					fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line(path))
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", err)
				return err
			}
			if closer != nil {
				defer closer.Close()
			}

			// With a timezone, run the DST-aware path; otherwise the plain
			// default-rule lint (UTC, no DST findings) for backward compatibility.
			var report lint.Report
			if loc != nil {
				rules := lint.FilterRules(lint.DefaultRulesTZ(loc, time.Now()), cfg.Lint.Disable)
				report, err = lint.Lint(reader, rules)
			} else {
				rules := lint.FilterRules(lint.DefaultRules(), cfg.Lint.Disable)
				report, err = lint.Lint(reader, rules)
			}
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "error: reading crontab: %v\n", err)
				return err
			}

			_, warnings, errs := report.Counts()
			failing := ci && report.Worst() >= failLevel && (warnings > 0 || errs > 0)

			if asJSON {
				if err := writeLintJSON(cmd, src, report); err != nil {
					return err
				}
				if failing {
					return errCIThreshold
				}
				return nil
			}

			writeLintHuman(cmd, src, report, quiet)
			if failing {
				return errCIThreshold
			}
			return nil
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "emit a stable machine-readable JSON report")
	cmd.Flags().BoolVar(&ci, "ci", false, "exit non-zero when any warning or error is found")
	cmd.Flags().StringVar(&ciLevel, "ci-level", "warning",
		"severity threshold for --ci failure: 'warning' (warnings+errors) or 'error' (errors only); implies --ci")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "silence the goblin's grumbling (stderr persona)")
	cmd.Flags().StringVar(&tz, "tz", "",
		"timezone for DST-hazard analysis (IANA name, e.g. America/New_York; off when unset)")

	return cmd
}

// errCIThreshold is returned (after the report is printed) when --ci is set and
// the crontab has warnings or errors, so the process exits non-zero. The
// message is suppressed by SilenceErrors; main.go maps the error to exit 1.
var errCIThreshold = fmt.Errorf("lint found warnings or errors")

// parseCILevel maps the --ci-level string to the minimum Severity that should
// trip a non-zero exit. Empty defaults to "warning" (historical behavior).
// Unknown values are rejected so a typo can't silently weaken CI gating.
func parseCILevel(level string) (lint.Severity, error) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "", "warning", "warn":
		return lint.SeverityWarning, nil
	case "error", "err":
		return lint.SeverityError, nil
	default:
		return 0, fmt.Errorf("invalid --ci-level %q: want \"warning\" or \"error\"", level)
	}
}

// openCrontab resolves the input source. An empty path or "-" means stdin; the
// returned closer is nil in that case (we never close the shared stdin). For a
// file, the caller is responsible for closing via the returned io.Closer. src
// is a human label for the source ("<stdin>" or the file path).
func openCrontab(cmd *cobra.Command, path string) (src string, r io.Reader, closer io.Closer, err error) {
	if path == "" || path == "-" {
		return "<stdin>", cmd.InOrStdin(), nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", nil, nil, err
	}
	return path, f, f, nil
}

// writeLintJSON renders the report as indented JSON on stdout.
func writeLintJSON(cmd *cobra.Command, src string, report lint.Report) error {
	info, warning, errs := report.Counts()
	findings := make([]lintFindingJSON, len(report.Findings))
	for i, f := range report.Findings {
		lines := f.Lines
		if lines == nil {
			lines = []int{}
		}
		findings[i] = lintFindingJSON{
			Rule:     f.Rule,
			Severity: f.Severity.String(),
			Message:  f.Message,
			Lines:    lines,
		}
	}

	payload := lintReportJSON{
		Source:   src,
		Entries:  report.Entries,
		Findings: findings,
		Counts:   lintCountsJSON{Info: info, Warning: warning, Error: errs},
		Worst:    report.Worst().String(),
		OK:       warning == 0 && errs == 0,
	}
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(payload)
}

// writeLintHuman renders the report for a terminal: persona grumble on stderr,
// facts on stdout. Findings are grouped one-per-line with a severity tag and
// the source lines they reference, then a one-line summary.
func writeLintHuman(cmd *cobra.Command, src string, report lint.Report, quiet bool) {
	out := cmd.OutOrStdout()
	info, warning, errs := report.Counts()

	if !quiet {
		// Keep the goblin's reaction proportional: snark scales with severity.
		fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line(src+report.Worst().String()))
	}

	fmt.Fprintf(out, "Linted %s — %d schedule(s).\n", src, report.Entries)

	if len(report.Findings) == 0 {
		fmt.Fprintln(out, "No problems found. Suspiciously clean.")
		return
	}

	fmt.Fprintln(out)
	for _, f := range report.Findings {
		fmt.Fprintf(out, "  [%s] %s%s\n",
			strings.ToUpper(f.Severity.String()), lineTag(f.Lines), f.Message)
	}

	fmt.Fprintf(out, "\nSummary: %d error(s), %d warning(s), %d info.\n", errs, warning, info)
}

// lineTag renders a finding's source lines as a "line N: " / "lines N, M: "
// prefix, or "" when the finding references no specific line.
func lineTag(lines []int) string {
	switch len(lines) {
	case 0:
		return ""
	case 1:
		return fmt.Sprintf("line %d: ", lines[0])
	default:
		parts := make([]string, len(lines))
		for i, l := range lines {
			parts[i] = fmt.Sprintf("%d", l)
		}
		return "lines " + strings.Join(parts, ", ") + ": "
	}
}
