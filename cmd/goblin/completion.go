// completion.go implements `goblin completion <shell>`: emit a shell completion
// script for bash, zsh, fish, or PowerShell to stdout.
//
// Cobra can auto-generate a generic `completion` command, but it ships with
// terse, brand-less help and no opinion about where the script should go. This
// replaces it with a goblin-flavored command that (a) documents the exact
// install incantation for each shell right in `--help`, and (b) keeps the
// generated script itself byte-for-byte whatever cobra produces — we only own
// the wrapping and the prose, never the completion logic.
//
//	goblin completion bash  > /etc/bash_completion.d/goblin
//	goblin completion zsh   > "${fpath[1]}/_goblin"
//	goblin completion fish  > ~/.config/fish/completions/goblin.fish
//	goblin completion powershell | Out-String | Invoke-Expression
package main

import (
	"fmt"

	"github.com/rwrife/cron-goblin/internal/goblin"
	"github.com/spf13/cobra"
)

// completionShells is the ordered, canonical set of shells we generate scripts
// for. It backs both the per-shell subcommands and the argument completion on
// the parent command, so the two can never drift out of sync.
var completionShells = []string{"bash", "zsh", "fish", "powershell"}

// newCompletionCmd builds the `completion` command tree. It mirrors cobra's
// built-in generator (one hidden-free subcommand per shell) but with the
// goblin's voice and real, copy-pasteable install docs.
//
// It takes the root command rather than reaching for cmd.Root() at run time so
// the generators always target the fully-wired tree (tests build a fresh root,
// and a detached completion command would otherwise generate scripts for the
// wrong parent).
func newCompletionCmd(root *cobra.Command) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "completion <bash|zsh|fish|powershell>",
		Short: "Generate a shell completion script (bash, zsh, fish, powershell)",
		Long: "Generate a shell completion script so your shell can tab-complete\n" +
			"goblin's subcommands, flags, and the shells listed here.\n\n" +
			"The script is written to stdout; redirect it wherever your shell\n" +
			"looks for completions. The goblin's grumbling (if any) goes to\n" +
			"stderr, so the redirected file stays clean.\n\n" +
			"Pick your shell's subcommand below for the exact install line.",
		Example: "  goblin completion bash > /etc/bash_completion.d/goblin\n" +
			"  goblin completion zsh  > \"${fpath[1]}/_goblin\"\n" +
			"  goblin completion fish > ~/.config/fish/completions/goblin.fish\n" +
			"  goblin completion powershell | Out-String | Invoke-Expression",
		// A bare `goblin completion` is a usage error, not a no-op: name a shell.
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		// Show help when invoked with no shell, matching the other parent-only
		// commands' behavior rather than dumping an empty script.
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(
		newCompletionShellCmd(root, "bash",
			"Generate the bash completion script",
			"Load goblin's bash completions.\n\n"+
				"One-off (current shell only):\n"+
				"  source <(goblin completion bash)\n\n"+
				"Persist them. On most Linux distros:\n"+
				"  goblin completion bash | sudo tee /etc/bash_completion.d/goblin >/dev/null\n\n"+
				"On macOS (Homebrew bash-completion@2):\n"+
				"  goblin completion bash > $(brew --prefix)/etc/bash_completion.d/goblin\n\n"+
				"Requires the bash-completion package (v2) to be installed and\n"+
				"sourced from your ~/.bashrc. Start a new shell afterward.",
			func(c *cobra.Command) error {
				return root.GenBashCompletionV2(c.OutOrStdout(), true)
			},
		),
		newCompletionShellCmd(root, "zsh",
			"Generate the zsh completion script",
			"Load goblin's zsh completions.\n\n"+
				"If shell completion isn't already enabled, enable it once by adding\n"+
				"this to your ~/.zshrc:\n"+
				"  autoload -Uz compinit && compinit\n\n"+
				"Then drop the script somewhere on your $fpath, e.g.:\n"+
				"  goblin completion zsh > \"${fpath[1]}/_goblin\"\n\n"+
				"Start a new shell for it to take effect.",
			func(c *cobra.Command) error {
				return root.GenZshCompletion(c.OutOrStdout())
			},
		),
		newCompletionShellCmd(root, "fish",
			"Generate the fish completion script",
			"Load goblin's fish completions.\n\n"+
				"One-off (current shell only):\n"+
				"  goblin completion fish | source\n\n"+
				"Persist them:\n"+
				"  goblin completion fish > ~/.config/fish/completions/goblin.fish\n\n"+
				"fish loads these automatically in new shells.",
			func(c *cobra.Command) error {
				return root.GenFishCompletion(c.OutOrStdout(), true)
			},
		),
		newCompletionShellCmd(root, "powershell",
			"Generate the PowerShell completion script",
			"Load goblin's PowerShell completions.\n\n"+
				"One-off (current session only):\n"+
				"  goblin completion powershell | Out-String | Invoke-Expression\n\n"+
				"Persist them by appending that line to your PowerShell profile:\n"+
				"  goblin completion powershell >> $PROFILE\n\n"+
				"Reload your profile or start a new session afterward.",
			func(c *cobra.Command) error {
				return root.GenPowerShellCompletionWithDesc(c.OutOrStdout())
			},
		),
	)

	return cmd
}

// newCompletionShellCmd builds one per-shell generator subcommand. gen does the
// actual cobra generation against the root tree; everything else here is the
// shared shape (no args, persona on stderr, errors silenced for clean output).
func newCompletionShellCmd(root *cobra.Command, shell, short, long string, gen func(*cobra.Command) error) *cobra.Command {
	var quiet bool

	c := &cobra.Command{
		Use:           shell,
		Short:         short,
		Long:          long,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		// Don't offer file/flag completion *for the completion command itself* —
		// it takes no arguments. Avoids `goblin completion bash <TAB>` listing the
		// filesystem.
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Persona to stderr so the generated script on stdout stays pristine
			// and pipeable. Keyed by shell name so it's deterministic per shell.
			if !quiet {
				fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line("completion:"+shell))
			}
			return gen(cmd)
		},
	}

	// Local --quiet so `--quiet` works whether placed before or after the shell
	// name; the persistent root --quiet only binds when it precedes the subcmd.
	c.Flags().BoolVar(&quiet, "quiet", false, "silence the goblin's grumbling (stderr persona)")

	return c
}
