// stagger.go implements `goblin stagger`: read a crontab, find "thundering
// herds" (clusters of jobs that all fire on the exact same minute), and print a
// rewritten crontab that spreads each herd across a window so they no longer
// stampede the box together. This is the user-facing surface of the
// internal/stagger engine and the auto-stagger half of the M-backlog
// thundering-herd feature.
//
// Safety: stagger never rewrites your crontab in place unless you explicitly
// ask with --write AND confirm. By default it prints the proposed crontab to
// stdout (a dry run you can pipe or eyeball); --write with --yes (or an
// interactive "yes") is required before any file is touched.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/rwrife/cron-goblin/internal/goblin"
	"github.com/rwrife/cron-goblin/internal/stagger"
	"github.com/spf13/cobra"
)

// staggerMoveJSON is the stable per-line shape in `stagger --json`.
type staggerMoveJSON struct {
	Line       int    `json:"line"`
	Command    string `json:"command"`
	FromMinute int    `json:"from_minute"`
	ToMinute   int    `json:"to_minute"`
	Original   string `json:"original"`
	Rewritten  string `json:"rewritten"`
}

// staggerHerdJSON groups the moves for one detected herd.
type staggerHerdJSON struct {
	Signature string            `json:"signature"`
	Moves     []staggerMoveJSON `json:"moves"`
}

// staggerReportJSON is the machine-readable shape emitted by `stagger --json`.
// Small and stable so agents/CI can depend on it. Rewritten is the full
// proposed crontab text; moved counts only lines whose minute actually changed.
type staggerReportJSON struct {
	Source    string            `json:"source"`
	MaxSpread int               `json:"max_spread"`
	Herds     []staggerHerdJSON `json:"herds"`
	Moved     int               `json:"moved"`
	OK        bool              `json:"ok"`
	Rewritten string            `json:"rewritten"`
}

// newStaggerCmd builds the `stagger` subcommand.
func newStaggerCmd() *cobra.Command {
	var (
		asJSON    bool
		quiet     bool
		maxSpread int
		write     bool
		assumeYes bool
	)

	cmd := &cobra.Command{
		Use:   "stagger [crontab-file]",
		Short: "Spread thundering herds: de-collide jobs that all fire on the same minute",
		Long: "Read a crontab (a file path, or stdin when omitted or '-') and find\n" +
			"\"thundering herds\" — clusters of jobs that fire on the exact same\n" +
			"minute (the classic `0 9 * * *` pile-up). For each herd, propose a\n" +
			"deterministic, evenly spaced spread across a window (default 59\n" +
			"minutes) so the jobs no longer stampede together.\n\n" +
			"Only jobs whose minute is a single fixed value (e.g. `0`, `30`) and\n" +
			"whose other fields match are staggered — spreading the minute keeps\n" +
			"each job in the same hour/day it already had. Jobs with stepped or\n" +
			"listed minutes (`*/15`, `0,30`) are left alone.\n\n" +
			"By default the rewritten crontab is printed to stdout (a dry run).\n" +
			"Pass --write with --yes to overwrite the source file in place; the\n" +
			"goblin never edits your crontab without that explicit confirmation.",
		Example: "  goblin stagger crontab.txt                 # preview the spread\n" +
			"  crontab -l | goblin stagger -              # de-herd your live crontab (dry run)\n" +
			"  goblin stagger --max-spread 30 crontab.txt # spread within 30 minutes\n" +
			"  goblin stagger --json crontab.txt          # machine-readable plan\n" +
			"  goblin stagger --write --yes crontab.txt   # rewrite the file in place",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := ""
			if len(args) == 1 {
				path = args[0]
			}

			src, label, err := readCrontabAll(cmd, path)
			if err != nil {
				if !quiet {
					fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line(path))
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", err)
				return err
			}

			plan, err := stagger.Analyze(src, maxSpread)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "error: reading crontab: %v\n", err)
				return err
			}
			rewritten := plan.Rewrite(src)

			if asJSON {
				if err := writeStaggerJSON(cmd, label, plan, rewritten); err != nil {
					return err
				}
				// --write still honored alongside --json so agents can apply.
				if write {
					return applyStagger(cmd, path, label, rewritten, plan, assumeYes, true)
				}
				return nil
			}

			if write {
				return applyStagger(cmd, path, label, rewritten, plan, assumeYes, false)
			}

			writeStaggerHuman(cmd, label, plan, rewritten, quiet)
			return nil
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "emit a stable machine-readable JSON plan")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "silence the goblin's grumbling (stderr persona)")
	cmd.Flags().IntVar(&maxSpread, "max-spread", stagger.DefaultMaxSpread,
		"maximum window, in minutes, to spread each herd across")
	cmd.Flags().BoolVar(&write, "write", false,
		"overwrite the source crontab file in place (requires a file path and confirmation)")
	cmd.Flags().BoolVar(&assumeYes, "yes", false,
		"skip the confirmation prompt when used with --write")

	return cmd
}

// readCrontabAll slurps the whole crontab source into a string. An empty path
// or "-" reads stdin; otherwise it reads the named file. label is a human
// source name ("<stdin>" or the path). Reading it all (rather than streaming)
// is necessary because a rewrite must preserve every original line verbatim.
func readCrontabAll(cmd *cobra.Command, path string) (content, label string, err error) {
	if path == "" || path == "-" {
		var sb strings.Builder
		sc := bufio.NewScanner(cmd.InOrStdin())
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		first := true
		for sc.Scan() {
			if !first {
				sb.WriteByte('\n')
			}
			sb.WriteString(sc.Text())
			first = false
		}
		if err := sc.Err(); err != nil {
			return "", "<stdin>", err
		}
		return sb.String(), "<stdin>", nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", path, err
	}
	return string(b), path, nil
}

// applyStagger writes the rewritten crontab back to the source file, after
// confirmation. It refuses to write to stdin (no file to overwrite) and, when
// the plan is a no-op, says so without touching the file. When jsonMode is true
// the human confirmation chatter is suppressed (the JSON was already emitted),
// but the safety gate still applies: without --yes on a non-TTY, it declines.
func applyStagger(cmd *cobra.Command, path, label, rewritten string, plan stagger.Plan, assumeYes, jsonMode bool) error {
	if path == "" || path == "-" {
		err := fmt.Errorf("--write needs a crontab file path; refusing to overwrite stdin")
		fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", err)
		return err
	}
	if plan.Empty() {
		if !jsonMode {
			fmt.Fprintf(cmd.OutOrStdout(), "%s is already well spread — nothing to write.\n", label)
		}
		return nil
	}

	if !assumeYes {
		ok, err := confirm(cmd, fmt.Sprintf(
			"Rewrite %s in place, moving %d job(s)? [y/N] ", label, plan.MovedLines()))
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", err)
			return err
		}
		if !ok {
			fmt.Fprintln(cmd.OutOrStdout(), "Aborted — crontab left untouched.")
			return nil
		}
	}

	if err := os.WriteFile(path, []byte(rewritten), 0o644); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "error: writing %s: %v\n", label, err)
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Rewrote %s — staggered %d job(s) across %d herd(s).\n",
		label, plan.MovedLines(), len(plan.Herds))
	return nil
}

// confirm prompts on stdout and reads a yes/no answer from the command's input.
// It returns true only for an explicit affirmative ("y"/"yes", case-insensitive)
// so the default — including a bare Enter or EOF on a non-TTY — is "no". This is
// the gate that keeps --write from ever silently overwriting a crontab.
func confirm(cmd *cobra.Command, prompt string) (bool, error) {
	fmt.Fprint(cmd.OutOrStdout(), prompt)
	sc := bufio.NewScanner(cmd.InOrStdin())
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return false, err
		}
		return false, nil // EOF / no input → treat as "no"
	}
	answer := strings.ToLower(strings.TrimSpace(sc.Text()))
	return answer == "y" || answer == "yes", nil
}

// writeStaggerJSON renders the plan as indented JSON on stdout.
func writeStaggerJSON(cmd *cobra.Command, src string, plan stagger.Plan, rewritten string) error {
	herds := make([]staggerHerdJSON, len(plan.Herds))
	for i, h := range plan.Herds {
		moves := make([]staggerMoveJSON, len(h.Moves))
		for j, m := range h.Moves {
			moves[j] = staggerMoveJSON{
				Line:       m.Line,
				Command:    m.Command,
				FromMinute: m.FromMinute,
				ToMinute:   m.ToMinute,
				Original:   m.Original,
				Rewritten:  m.Rewritten,
			}
		}
		herds[i] = staggerHerdJSON{Signature: h.Signature, Moves: moves}
	}

	payload := staggerReportJSON{
		Source:    src,
		MaxSpread: plan.MaxSpread,
		Herds:     herds,
		Moved:     plan.MovedLines(),
		OK:        plan.Empty(),
		Rewritten: rewritten,
	}
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(payload)
}

// writeStaggerHuman renders the plan for a terminal: persona grumble on stderr,
// the proposed changes and the full rewritten crontab on stdout. When there is
// no herd it says so and prints nothing else; the goblin is briefly pleased.
func writeStaggerHuman(cmd *cobra.Command, src string, plan stagger.Plan, rewritten string, quiet bool) {
	out := cmd.OutOrStdout()

	if !quiet {
		fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line(src+"stagger"))
	}

	if plan.Empty() {
		fmt.Fprintf(out, "No thundering herds in %s — already nicely spread.\n", src)
		return
	}

	herdWord := "herd"
	if len(plan.Herds) != 1 {
		herdWord = "herds"
	}
	fmt.Fprintf(out, "Found %d %s in %s (spreading across %d min):\n",
		len(plan.Herds), herdWord, src, plan.MaxSpread)

	for _, h := range plan.Herds {
		fmt.Fprintf(out, "\n  %d jobs on `%s`:\n", len(h.Moves), h.Signature)
		for _, m := range h.Moves {
			if m.FromMinute == m.ToMinute {
				fmt.Fprintf(out, "    line %d: minute %d (anchor, unchanged) — %s\n",
					m.Line, m.FromMinute, m.Command)
			} else {
				fmt.Fprintf(out, "    line %d: minute %d → %d — %s\n",
					m.Line, m.FromMinute, m.ToMinute, m.Command)
			}
		}
	}

	fmt.Fprintf(out, "\nProposed crontab (%d job(s) moved). Re-run with --write --yes to apply:\n\n",
		plan.MovedLines())
	fmt.Fprintln(out, rewritten)
}
