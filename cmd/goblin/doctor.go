// doctor.go implements `goblin doctor`: the zero-argument convenience wrapper
// around `goblin lint`. Instead of pointing at a file, it reads the *current
// user's* installed crontab (via `crontab -l`) and runs the same lint engine
// over it. It's the "just check my actual cron jobs" button.
//
//	goblin doctor            # lint your own crontab
//	goblin doctor --json     # stable report for scripts/agents
//	goblin doctor --ci       # non-zero exit if any warning/error
//
// It deliberately shares the lint renderers (writeLintJSON/writeLintHuman) so
// `doctor` and `lint` produce byte-identical reports for the same crontab.
package main

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/rwrife/cron-goblin/internal/goblin"
	"github.com/rwrife/cron-goblin/internal/lint"
	"github.com/spf13/cobra"
)

// errNoCrontab signals that the target user simply has no crontab installed.
// That's not a failure of the tool — it's a clean, empty result — so doctor
// reports it calmly and exits zero (even under --ci: nothing is wrong).
var errNoCrontab = errors.New("no crontab installed for this user")

// crontabLoader fetches a user's raw crontab text. It is a package var so tests
// can substitute a fake loader instead of depending on a real `crontab` binary
// (which CI runners and containers often lack). user is empty for "the current
// user"; a non-empty user maps to `crontab -u <user> -l`.
//
// It returns errNoCrontab when the user has no crontab, so the command can tell
// "you have nothing scheduled" apart from "crontab is broken/missing".
var crontabLoader = loadUserCrontab

// newDoctorCmd builds the `doctor` subcommand.
func newDoctorCmd() *cobra.Command {
	var (
		asJSON bool
		ci     bool
		quiet  bool
		user   string
	)

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Lint the current user's installed crontab",
		Long: "Read your installed crontab with `crontab -l` and check it like a\n" +
			"linter checks code — the same rules as `goblin lint`, but pointed at\n" +
			"the cron jobs you actually have rather than a file you name:\n\n" +
			"  • dead-expression — schedules that can never fire (error)\n" +
			"  • too-frequent    — every-minute / runaway cadences (warning)\n" +
			"  • collision       — jobs that fire at the same instant (warning)\n\n" +
			"Use --json for a stable machine-readable report, --ci to exit non-zero\n" +
			"on any warning or error, and --user to inspect another account's\n" +
			"crontab (usually needs root). A user with no crontab is reported\n" +
			"calmly and exits zero — nothing scheduled is nothing to fix.",
		Example: "  goblin doctor\n" +
			"  goblin doctor --json\n" +
			"  goblin doctor --ci            # non-zero exit on warnings/errors\n" +
			"  sudo goblin doctor --user deploy",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			src := crontabSource(user)

			// Project config (.goblinrc) can disable rules and enable CI mode
			// as defaults, under explicit flags. --no-config bypasses it.
			cfg, cerr := loadConfig(cmd, quiet)
			if cerr != nil {
				return cerr
			}
			if cfg.CIEnabled() {
				ci = true
			}

			raw, err := crontabLoader(user)
			if err != nil {
				if errors.Is(err, errNoCrontab) {
					// No crontab is a clean, empty result — not an error. Say so
					// (with a calmer-than-usual grumble) and exit zero.
					if !quiet {
						fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line(src))
					}
					fmt.Fprintf(cmd.OutOrStdout(),
						"No crontab installed for %s — nothing to lint.\n", src)
					return nil
				}
				if !quiet {
					fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line(src))
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", err)
				return err
			}

			rules := lint.FilterRules(lint.DefaultRules(), cfg.Lint.Disable)
			report, err := lint.Lint(strings.NewReader(raw), rules)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "error: reading crontab: %v\n", err)
				return err
			}

			_, warnings, errs := report.Counts()
			failing := ci && (warnings > 0 || errs > 0)

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
	cmd.Flags().BoolVar(&quiet, "quiet", false, "silence the goblin's grumbling (stderr persona)")
	cmd.Flags().StringVar(&user, "user", "", "inspect another user's crontab via `crontab -u` (usually needs root)")

	return cmd
}

// crontabSource returns the human-facing label for the report's source line,
// mirroring the "<stdin>" / file-path labels used by `lint`.
func crontabSource(user string) string {
	if user == "" {
		return "<crontab>"
	}
	return "<crontab:" + user + ">"
}

// loadUserCrontab shells out to `crontab -l` (optionally `-u <user>`) and
// returns the raw crontab text. It distinguishes three outcomes:
//
//   - success: the crontab text, nil error.
//   - the user has no crontab: "", errNoCrontab. `crontab -l` exits non-zero
//     and prints a "no crontab for <user>" message to stderr in this case, so
//     we detect that message rather than treating every non-zero exit as fatal.
//   - anything else (no `crontab` binary, permission denied, ...): "", a
//     descriptive error.
func loadUserCrontab(user string) (string, error) {
	args := []string{"-l"}
	if user != "" {
		args = []string{"-u", user, "-l"}
	}

	cmd := exec.Command("crontab", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err == nil {
		return stdout.String(), nil
	}

	// `crontab -l` with no installed crontab exits non-zero and says so on
	// stderr ("no crontab for <user>"). Treat that as the empty/clean case.
	if isNoCrontabMessage(stderr.String()) {
		return "", errNoCrontab
	}

	// The binary itself is missing — give a clearer hint than exec's default.
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return "", fmt.Errorf("could not run `crontab`: %w (is cron installed and on PATH?)", err)
	}

	// Some other failure (permissions, unknown user, ...): surface crontab's
	// own stderr when it gave us one, otherwise the raw exec error.
	if msg := strings.TrimSpace(stderr.String()); msg != "" {
		return "", fmt.Errorf("crontab -l failed: %s", msg)
	}
	return "", fmt.Errorf("crontab -l failed: %w", err)
}

// isNoCrontabMessage reports whether crontab's stderr indicates the user simply
// has no crontab. Different cron implementations phrase this slightly
// differently, but all the common ones contain "no crontab for".
func isNoCrontabMessage(stderr string) bool {
	return strings.Contains(strings.ToLower(stderr), "no crontab for")
}
