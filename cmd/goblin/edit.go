// edit.go implements `goblin edit`: the author-time loop that `crontab -e`
// fatally lacks. It opens a crontab in $EDITOR, then lints it on save. If lint
// finds problems at or above the configured threshold, it shows the goblin's
// snark and re-prompts (edit again / install anyway / abort) instead of
// silently installing a broken crontab.
//
//	goblin edit                 # edit your live crontab (crontab -l -> tmp -> edit)
//	goblin edit crontab.txt     # edit a file in place, re-lint on save
//	goblin edit --no-install    # dry-run: edit + lint, never touch the crontab
//	goblin edit --json          # machine summary of the change + lint result
//
// It deliberately reuses the same lint engine and rule set as `lint`/`doctor`
// so the three commands agree on the same input.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/rwrife/cron-goblin/internal/config"
	"github.com/rwrife/cron-goblin/internal/goblin"
	"github.com/rwrife/cron-goblin/internal/lint"
	"github.com/spf13/cobra"
)

// editorRunner opens path in the user's editor and blocks until it exits. It is
// a package var so tests can substitute a fake editor (e.g. a func that
// rewrites the file) instead of spawning a real $EDITOR.
var editorRunner = runEditor

// crontabInstaller installs raw crontab text via `crontab -`. It is a package
// var so tests can capture the install without touching a real crontab.
var crontabInstaller = installCrontab

// editResultJSON is the stable machine-readable shape emitted by `edit --json`.
type editResultJSON struct {
	Source    string         `json:"source"`
	Changed   bool           `json:"changed"`
	Installed bool           `json:"installed"`
	Lint      lintReportJSON `json:"lint"`
}

// editAction is the user's choice at the re-prompt when lint finds problems.
type editAction int

const (
	actionEditAgain editAction = iota
	actionInstallAnyway
	actionAbort
)

// newEditCmd builds the `edit` subcommand.
func newEditCmd() *cobra.Command {
	var (
		asJSON    bool
		quiet     bool
		tz        string
		ciLevel   string
		noInstall bool
	)

	cmd := &cobra.Command{
		Use:   "edit [crontab-file]",
		Short: "Edit a crontab in $EDITOR, then lint it before installing",
		Long: "Close the author-time loop that `crontab -e` skips: open a crontab in\n" +
			"$EDITOR (fallback `vi`), and re-lint the moment you save. If lint finds\n" +
			"problems at or above the threshold, the goblin grumbles and re-prompts\n" +
			"(edit again / install anyway / abort) instead of silently installing a\n" +
			"crontab that never fires.\n\n" +
			"With no file argument, edit your live crontab: `crontab -l` is dumped to\n" +
			"a temp file, edited, then offered back to `crontab -`. With a file, edit\n" +
			"it in place. Use --no-install to only lint (never touch the live crontab)\n" +
			"and --json for a stable summary of what changed plus the lint result.",
		Example: "  goblin edit\n" +
			"  goblin edit crontab.txt\n" +
			"  goblin edit --no-install crontab.txt   # lint only, don't install\n" +
			"  goblin edit --ci-level error           # only block on errors\n" +
			"  goblin edit --json",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := ""
			if len(args) == 1 {
				path = args[0]
			}

			// Config (.goblinrc) presets tz, ci-level, and disabled rules under
			// explicit flags, matching `lint`.
			cfg, cerr := loadConfig(cmd, quiet)
			if cerr != nil {
				return cerr
			}
			tz = resolveTZ(tz, cmd.Flags().Changed("tz"), cfg)
			if !cmd.Flags().Changed("ci-level") && cfg.Lint.CILevel != "" {
				ciLevel = cfg.Lint.CILevel
			}
			failLevel, err := parseCILevel(ciLevel)
			if err != nil {
				if !quiet {
					fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line(ciLevel))
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", err)
				return err
			}

			var loc *time.Location
			if tz != "" {
				l, lerr := loadLocation(tz)
				if lerr != nil {
					if !quiet {
						fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line(tz))
					}
					fmt.Fprintf(cmd.ErrOrStderr(), "error: unknown timezone %q: %v\n", tz, lerr)
					return lerr
				}
				loc = l
			}

			// Resolve the editing target: a named file (edited in place) or the
			// live crontab dumped to a temp file we manage.
			live := path == ""
			src := path
			original := ""
			workPath := path
			if live {
				src = crontabSource("")
				raw, lerr := crontabLoader("")
				if lerr != nil && !errors.Is(lerr, errNoCrontab) {
					if !quiet {
						fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line(src))
					}
					fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", lerr)
					return lerr
				}
				original = raw
				tmp, terr := os.CreateTemp("", "goblin-crontab-*.cron")
				if terr != nil {
					return fmt.Errorf("creating temp crontab: %w", terr)
				}
				workPath = tmp.Name()
				defer os.Remove(workPath)
				if _, werr := tmp.WriteString(raw); werr != nil {
					tmp.Close()
					return fmt.Errorf("writing temp crontab: %w", werr)
				}
				tmp.Close()
			} else {
				b, rerr := os.ReadFile(path)
				if rerr != nil {
					if !quiet {
						fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line(path))
					}
					fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", rerr)
					return rerr
				}
				original = string(b)
			}

			rules := buildLintRules(loc, cfg)

			// Edit -> lint -> prompt loop.
			var report lint.Report
			var edited string
			blockedByLint := false
			for {
				if eerr := editorRunner(workPath); eerr != nil {
					if !quiet {
						fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line(src))
					}
					fmt.Fprintf(cmd.ErrOrStderr(), "error: editor: %v\n", eerr)
					return eerr
				}
				b, rerr := os.ReadFile(workPath)
				if rerr != nil {
					return fmt.Errorf("reading edited crontab: %w", rerr)
				}
				edited = string(b)

				report, err = lint.Lint(strings.NewReader(edited), rules)
				if err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "error: reading crontab: %v\n", err)
					return err
				}

				// Clean enough? Nothing at/above the threshold -> done.
				if report.Worst() < failLevel {
					break
				}

				// JSON mode is non-interactive: report and stop without
				// installing. The caller (agent/CI) inspects .lint and decides.
				if asJSON {
					blockedByLint = true
					break
				}

				writeLintHuman(cmd, src, report, quiet)
				action, aerr := promptEditAction(cmd, quiet)
				if aerr != nil {
					return aerr
				}
				if action == actionEditAgain {
					continue
				}
				if action == actionAbort {
					fmt.Fprintln(cmd.OutOrStdout(), "Aborted. Nothing installed.")
					if asJSON {
						break
					}
					return nil
				}
				// actionInstallAnyway: fall through to install.
				break
			}

			changed := edited != original
			installed := false

			// Install path: only for the live crontab, only when it changed,
			// and never under --no-install. A named-file edit already wrote to
			// the file via the editor, so there's nothing extra to install.
			if live && !noInstall && changed && !blockedByLint {
				if ierr := crontabInstaller(edited); ierr != nil {
					if !quiet {
						fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line(src))
					}
					fmt.Fprintf(cmd.ErrOrStderr(), "error: installing crontab: %v\n", ierr)
					return ierr
				}
				installed = true
			}

			if asJSON {
				return writeEditJSON(cmd, src, changed, installed, report)
			}

			writeEditHuman(cmd, src, report, changed, installed, live, noInstall, quiet)
			return nil
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "emit a stable machine-readable summary (non-interactive)")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "silence the goblin's grumbling (stderr persona)")
	cmd.Flags().StringVar(&tz, "tz", "",
		"timezone for DST-hazard analysis (IANA name, e.g. America/New_York; off when unset)")
	cmd.Flags().StringVar(&ciLevel, "ci-level", "warning",
		"severity threshold that triggers the re-prompt: 'warning' (warnings+errors) or 'error' (errors only)")
	cmd.Flags().BoolVar(&noInstall, "no-install", false,
		"lint only: never write back to the live crontab (dry run)")

	return cmd
}

// buildLintRules assembles the effective rule set for edit, mirroring lint's
// choice of DST-aware vs plain rules and applying config disables.
func buildLintRules(loc *time.Location, cfg config.Config) []lint.Rule {
	var base []lint.Rule
	if loc != nil {
		base = lint.DefaultRulesTZ(loc, time.Now())
	} else {
		base = lint.DefaultRules()
	}
	return lint.FilterRules(base, cfg.Lint.Disable)
}

// promptEditAction shows the edit-again / install-anyway / abort menu on stderr
// and reads a single choice from stdin. Any unrecognized input re-prompts.
// EOF (e.g. a closed pipe) defaults to abort — the safe choice.
func promptEditAction(cmd *cobra.Command, quiet bool) (editAction, error) {
	reader := bufio.NewReader(cmd.InOrStdin())
	for {
		if !quiet {
			fmt.Fprint(cmd.ErrOrStderr(),
				"\n[e]dit again / install [a]nyway / abo[r]t? ")
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				// No interactive input available: don't install junk.
				return actionAbort, nil
			}
			return actionAbort, err
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "e", "edit":
			return actionEditAgain, nil
		case "a", "anyway", "install":
			return actionInstallAnyway, nil
		case "r", "abort", "q", "quit":
			return actionAbort, nil
		}
	}
}

// runEditor launches $EDITOR (fallback $VISUAL, then `vi`) on path, wiring the
// child to the real terminal so the user can actually edit.
func runEditor(path string) error {
	editor := firstNonEmpty(os.Getenv("VISUAL"), os.Getenv("EDITOR"), "vi")
	// Support editors configured with args, e.g. EDITOR="code --wait".
	fields := strings.Fields(editor)
	if len(fields) == 0 {
		fields = []string{"vi"}
	}
	args := append(fields[1:], path)
	c := exec.Command(fields[0], args...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

// installCrontab pipes raw text into `crontab -`, replacing the current user's
// crontab. Any stderr from crontab is surfaced on failure.
func installCrontab(raw string) error {
	c := exec.Command("crontab", "-")
	c.Stdin = strings.NewReader(raw)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		var execErr *exec.Error
		if errors.As(err, &execErr) {
			return fmt.Errorf("could not run `crontab`: %w (is cron installed and on PATH?)", err)
		}
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("crontab - failed: %s", msg)
		}
		return fmt.Errorf("crontab - failed: %w", err)
	}
	return nil
}

// firstNonEmpty returns the first non-empty string, or "" if all are empty.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// writeEditJSON renders the edit summary as indented JSON, embedding the same
// lint report shape as `lint --json`.
func writeEditJSON(cmd *cobra.Command, src string, changed, installed bool, report lint.Report) error {
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
	payload := editResultJSON{
		Source:    src,
		Changed:   changed,
		Installed: installed,
		Lint: lintReportJSON{
			Source:   src,
			Entries:  report.Entries,
			Findings: findings,
			Counts:   lintCountsJSON{Info: info, Warning: warning, Error: errs},
			Worst:    report.Worst().String(),
			OK:       warning == 0 && errs == 0,
		},
	}
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(payload)
}

// writeEditHuman renders the terminal summary after an edit session: the lint
// findings (reusing the shared renderer) plus a one-line outcome.
func writeEditHuman(cmd *cobra.Command, src string, report lint.Report, changed, installed, live, noInstall, quiet bool) {
	writeLintHuman(cmd, src, report, quiet)

	out := cmd.OutOrStdout()
	switch {
	case installed:
		fmt.Fprintln(out, "\nInstalled the edited crontab.")
	case !changed:
		fmt.Fprintln(out, "\nNo changes made.")
	case live && noInstall:
		fmt.Fprintln(out, "\nChanged, but --no-install set: crontab left untouched.")
	case live:
		fmt.Fprintln(out, "\nChanged, but not installed.")
	default:
		fmt.Fprintln(out, "\nSaved.")
	}
}
