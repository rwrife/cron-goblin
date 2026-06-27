package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// runRootArgs runs the *full* root command tree with the given args and
// captures both streams. Completion generation has to go through the real root
// (not a detached completion command) because cobra walks the whole tree to
// build the script, so we exercise it the same way a user would.
func runRootArgs(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := newRootCmd("test-version")
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

// wantInScript holds a couple of stable markers we expect to see in each
// shell's generated completion script. We deliberately assert on cobra's own
// output markers (not exact bytes) so a cobra version bump that tweaks
// whitespace doesn't break us — we only care that it's the right shell's script
// and that it's wired to the `goblin` command.
var completionScriptMarkers = map[string][]string{
	"bash":       {"bash completion V2 for goblin", "__start_goblin"},
	"zsh":        {"#compdef goblin", "_goblin"},
	"fish":       {"fish completion for goblin", "goblin"},
	"powershell": {"powershell completion for goblin", "goblin"},
}

func TestCompletionGeneratesEachShell(t *testing.T) {
	for _, shell := range completionShells {
		shell := shell
		t.Run(shell, func(t *testing.T) {
			stdout, stderr, err := runRootArgs(t, "completion", shell)
			if err != nil {
				t.Fatalf("completion %s: unexpected error: %v", shell, err)
			}
			if strings.TrimSpace(stdout) == "" {
				t.Fatalf("completion %s: produced empty script", shell)
			}
			for _, marker := range completionScriptMarkers[shell] {
				if !strings.Contains(stdout, marker) {
					t.Errorf("completion %s: script missing marker %q\n--- script head ---\n%s",
						shell, marker, headLines(stdout, 5))
				}
			}
			// The persona must land on stderr, never in the script itself, so the
			// redirected file stays runnable.
			if strings.TrimSpace(stderr) == "" {
				t.Errorf("completion %s: expected goblin persona on stderr, got none", shell)
			}
		})
	}
}

// TestCompletionQuietSilencesPersona verifies --quiet keeps stderr clean while
// still emitting the script, whether the flag comes before or after the shell.
func TestCompletionQuietSilencesPersona(t *testing.T) {
	cases := [][]string{
		{"completion", "bash", "--quiet"},
		{"completion", "--quiet", "bash"},
	}
	for _, args := range cases {
		args := args
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			stdout, stderr, err := runRootArgs(t, args...)
			if err != nil {
				t.Fatalf("%v: unexpected error: %v", args, err)
			}
			if strings.TrimSpace(stderr) != "" {
				t.Errorf("%v: --quiet should silence stderr, got:\n%s", args, stderr)
			}
			if !strings.Contains(stdout, "bash completion V2 for goblin") {
				t.Errorf("%v: still expected a bash script on stdout", args)
			}
		})
	}
}

// TestCompletionScriptIsDeterministic guards the "stdout is clean and stable"
// promise: two runs of the same shell must produce byte-identical scripts (we
// own none of the generation, but a regression that leaked the persona or a
// timestamp into stdout would show up here).
func TestCompletionScriptIsDeterministic(t *testing.T) {
	first, _, err := runRootArgs(t, "completion", "zsh", "--quiet")
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	second, _, err := runRootArgs(t, "completion", "zsh", "--quiet")
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if first != second {
		t.Errorf("zsh completion script not deterministic across runs")
	}
}

// TestCompletionBareShowsHelp checks that `goblin completion` with no shell is a
// friendly help dump (exit 0, usage on stdout), not an empty script.
func TestCompletionBareShowsHelp(t *testing.T) {
	stdout, _, err := runRootArgs(t, "completion")
	if err != nil {
		t.Fatalf("bare completion: unexpected error: %v", err)
	}
	for _, want := range []string{"Usage:", "bash", "zsh", "fish", "powershell"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("bare completion help missing %q, got:\n%s", want, stdout)
		}
	}
}

// TestCompletionUnknownShellErrors makes sure an unsupported shell is rejected
// rather than silently emitting nothing.
func TestCompletionUnknownShellErrors(t *testing.T) {
	_, _, err := runRootArgs(t, "completion", "tcsh")
	if err == nil {
		t.Fatalf("expected an error for an unknown shell, got nil")
	}
}

// TestCompletionRegisteredOnRoot is a small wiring guard: the default cobra
// completion command is disabled, so the one a user sees must be ours and must
// expose exactly the four documented shells as subcommands.
func TestCompletionRegisteredOnRoot(t *testing.T) {
	root := newRootCmd("test-version")

	var completion *cobra.Command
	for _, c := range root.Commands() {
		if c.Name() == "completion" {
			completion = c
			break
		}
	}
	if completion == nil {
		t.Fatal("root command has no `completion` subcommand")
	}
	if !completion.HasSubCommands() {
		t.Fatal("completion command has no shell subcommands")
	}

	got := map[string]bool{}
	for _, sub := range completion.Commands() {
		got[sub.Name()] = true
	}
	for _, shell := range completionShells {
		if !got[shell] {
			t.Errorf("completion is missing the %q shell subcommand", shell)
		}
	}
}

// headLines returns the first n lines of s, for compact failure messages.
func headLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
