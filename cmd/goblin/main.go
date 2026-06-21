// Command goblin is the cron-goblin CLI: a grumpy gremlin that translates cron
// gibberish into plain English, previews fire times, and lints your crontab.
//
// M1 ships only the scaffold: a cobra root command that greets you and reports
// its version. Subcommands (explain, next, lint, from) arrive in later
// milestones — see PLAN.md.
package main

import (
	"fmt"
	"os"

	"github.com/rwrife/cron-goblin/internal/goblin"
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
	var quiet bool

	cmd := &cobra.Command{
		Use:   "goblin",
		Short: "A grumpy gremlin that guards your crontab",
		Long: "cron-goblin 👹⏰\n\n" +
			"Translates cron gibberish into plain English, previews when jobs\n" +
			"fire, and shrieks when two of them collide at 3am. Design-time,\n" +
			"offline, account-free. It never runs your jobs — it just judges them.",
		Version: version,
		// No subcommand yet: greet, then show help so users see what's coming.
		RunE: func(cmd *cobra.Command, args []string) error {
			if !quiet {
				// Persona goes to stderr so stdout stays clean for scripts.
				fmt.Fprintln(cmd.ErrOrStderr(), goblin.Greeting(0))
			}
			fmt.Fprintf(cmd.OutOrStdout(), "cron-goblin %s\n", version)
			fmt.Fprintln(cmd.OutOrStdout(),
				"No subcommands yet — this is M1 (scaffold). See `goblin --help` and PLAN.md.")
			return nil
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.PersistentFlags().BoolVar(&quiet, "quiet", false,
		"silence the goblin's grumbling (stderr persona)")

	// Friendlier `--version` output than cobra's default "goblin version X".
	cmd.SetVersionTemplate("cron-goblin {{.Version}}\n")

	return cmd
}
