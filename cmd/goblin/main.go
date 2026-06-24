// Command goblin is the cron-goblin CLI: a grumpy gremlin that translates cron
// gibberish into plain English, previews fire times, and lints your crontab.
//
// M1 shipped the scaffold; M2 added `explain`; M3 added `next`; M4 adds
// `lint`; M5 adds the live TUI preview (launched when `goblin` is run with no
// subcommand on a terminal). Further subcommands (from) arrive in later
// milestones — see PLAN.md.
package main

import (
	"fmt"
	"os"

	"github.com/mattn/go-isatty"
	"github.com/rwrife/cron-goblin/internal/goblin"
	"github.com/rwrife/cron-goblin/internal/tui"
	"github.com/spf13/cobra"
)

// version is the build version. It is overridden at release time via
//
//	-ldflags "-X main.version=v1.2.3"
//
// and otherwise reports a dev placeholder.
var version = "0.1.0-dev"

func main() {
	if err := newRootCmd(version).Execute(); err != nil {
		// cobra already prints the error; just set a non-zero exit code.
		os.Exit(1)
	}
}

// newRootCmd builds the root command. It is a constructor (rather than a
// package-level var) so tests can build a fresh command tree with a known
// version and capture its output.
func newRootCmd(version string) *cobra.Command {
	var (
		quiet   bool
		tz      string
		noColor bool
		noTUI   bool
	)

	cmd := &cobra.Command{
		Use:   "goblin",
		Short: "A grumpy gremlin that guards your crontab",
		Long: "cron-goblin 👹⏰\n\n" +
			"Translates cron gibberish into plain English, previews when jobs\n" +
			"fire, and shrieks when two of them collide at 3am. Design-time,\n" +
			"offline, account-free. It never runs your jobs — it just judges them.\n\n" +
			"Run with no arguments in a terminal to open the live preview TUI:\n" +
			"type a cron expression and watch the next runs and a week heatmap\n" +
			"update as you go.",
		Version: version,
		Args:    cobra.ArbitraryArgs,
		// With no subcommand: open the live TUI on a terminal; otherwise (piped
		// or redirected) keep the script-friendly greeting so non-TTY usage and
		// tests stay deterministic.
		RunE: func(cmd *cobra.Command, args []string) error {
			initial := ""
			if len(args) > 0 {
				initial = args[0]
			}

			if !noTUI && stdinIsTTY() && stdoutIsTTY() {
				loc, err := loadLocation(tz)
				if err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "error: unknown timezone %q: %v\n", tz, err)
					return err
				}
				return tui.Run(tui.Options{
					Initial:  initial,
					Location: loc,
					NoColor:  noColor,
				}, cmd.InOrStdin(), cmd.OutOrStdout())
			}

			// Non-TTY fallback: greet, then point at what's available.
			if !quiet {
				// Persona goes to stderr so stdout stays clean for scripts.
				fmt.Fprintln(cmd.ErrOrStderr(), goblin.Greeting(0))
			}
			fmt.Fprintf(cmd.OutOrStdout(), "cron-goblin %s\n", version)
			fmt.Fprintln(cmd.OutOrStdout(),
				"Try `goblin explain \"*/15 9-17 * * 1-5\"` or `goblin lint crontab.txt`, or run `goblin` in a terminal for the live preview. See `goblin --help` and PLAN.md.")
			return nil
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.PersistentFlags().BoolVar(&quiet, "quiet", false,
		"silence the goblin's grumbling (stderr persona)")
	cmd.Flags().StringVar(&tz, "tz", "",
		"timezone for the live preview (IANA name, e.g. America/New_York; default: local)")
	cmd.Flags().BoolVar(&noColor, "no-color", false,
		"disable ANSI color in the live preview")
	cmd.Flags().BoolVar(&noTUI, "no-tui", false,
		"never launch the live preview; print the text greeting instead")

	// Friendlier `--version` output than cobra's default "goblin version X".
	cmd.SetVersionTemplate("cron-goblin {{.Version}}\n")

	// Wire up subcommands.
	cmd.AddCommand(newExplainCmd())
	cmd.AddCommand(newNextCmd())
	cmd.AddCommand(newLintCmd())

	return cmd
}

// stdinIsTTY reports whether standard input is an interactive terminal. The
// no-argument root command only launches the live TUI when both stdin and
// stdout are TTYs; piped or redirected invocations get the script-friendly
// text greeting instead.
func stdinIsTTY() bool {
	return isatty.IsTerminal(os.Stdin.Fd()) || isatty.IsCygwinTerminal(os.Stdin.Fd())
}

// stdoutIsTTY reports whether standard output is an interactive terminal.
func stdoutIsTTY() bool {
	return isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd())
}
