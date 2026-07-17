package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// runEdit builds a fresh edit command with stubbed editor/installer/loader and
// captures its streams. Stubs are restored after the test. It mirrors runLint /
// runDoctor in the sibling test files.
func runEdit(t *testing.T, editor func(path string) error, args ...string) (stdout, stderr string, installed *string, err error) {
	t.Helper()

	prevEditor := editorRunner
	prevInstaller := crontabInstaller
	prevLoader := crontabLoader
	t.Cleanup(func() {
		editorRunner = prevEditor
		crontabInstaller = prevInstaller
		crontabLoader = prevLoader
	})

	var captured *string
	editorRunner = editor
	crontabInstaller = func(raw string) error {
		r := raw
		captured = &r
		return nil
	}

	cmd := newEditCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), errOut.String(), captured, err
}

// rewriteEditor returns a fake $EDITOR that overwrites the target file with the
// given content, simulating a user who edited and saved.
func rewriteEditor(content string) func(string) error {
	return func(path string) error {
		return os.WriteFile(path, []byte(content), 0o600)
	}
}

// noopEditor leaves the file untouched, simulating a user who saved without
// changes.
func noopEditor(string) error { return nil }

const cleanCrontab = "30 6 * * 1-5 /bin/report\n"
const warnCrontab = "* * * * * /bin/spin\n"
const errCrontab = "0 0 30 2 * /bin/never\n"

// --- file-mode tests -------------------------------------------------------

func TestEditFileCleanNoPrompt(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "cron-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("* * * * * /old\n")
	f.Close()

	stdout, _, _, err := runEdit(t, rewriteEditor(cleanCrontab), "--quiet", f.Name())
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !strings.Contains(stdout, "No problems found") {
		t.Errorf("expected clean lint, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "Saved.") {
		t.Errorf("expected saved outcome, got:\n%s", stdout)
	}
	// The editor wrote the file; confirm it stuck.
	b, _ := os.ReadFile(f.Name())
	if string(b) != cleanCrontab {
		t.Errorf("file not written as edited: %q", string(b))
	}
}

func TestEditFileWarningInstallAnyway(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "cron-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("# start\n")
	f.Close()

	// Interactive: warning triggers prompt; answer "a" (install anyway). File
	// mode installs nothing, but the prompt path must be exercised.
	prev := crontabLoader
	t.Cleanup(func() { crontabLoader = prev })

	editorRunner = rewriteEditor(warnCrontab)
	defer func() { editorRunner = noopEditor }()

	cmd := newEditCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetIn(strings.NewReader("a\n"))
	cmd.SetArgs([]string{f.Name()})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !strings.Contains(out.String(), "every minute") {
		t.Errorf("expected too-frequent warning, got:\n%s", out.String())
	}
}

func TestEditFileErrorAbort(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "cron-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("# start\n")
	f.Close()

	editorRunner = rewriteEditor(errCrontab)
	defer func() { editorRunner = noopEditor }()

	cmd := newEditCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetIn(strings.NewReader("r\n"))
	cmd.SetArgs([]string{f.Name()})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !strings.Contains(out.String(), "never fires") {
		t.Errorf("expected dead-expression error, got:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Aborted") {
		t.Errorf("expected abort outcome, got:\n%s", out.String())
	}
}

// --- live-mode tests -------------------------------------------------------

func TestEditLiveCleanInstalls(t *testing.T) {
	prev := crontabLoader
	crontabLoader = staticLoader("* * * * * /old\n")
	t.Cleanup(func() { crontabLoader = prev })

	stdout, _, installed, err := runEdit(t, rewriteEditor(cleanCrontab), "--quiet")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if installed == nil {
		t.Fatal("expected crontab to be installed, but installer was not called")
	}
	if *installed != cleanCrontab {
		t.Errorf("installed wrong content: %q", *installed)
	}
	if !strings.Contains(stdout, "Installed the edited crontab") {
		t.Errorf("expected install confirmation, got:\n%s", stdout)
	}
}

func TestEditLiveNoInstall(t *testing.T) {
	prev := crontabLoader
	crontabLoader = staticLoader("* * * * * /old\n")
	t.Cleanup(func() { crontabLoader = prev })

	stdout, _, installed, err := runEdit(t, rewriteEditor(cleanCrontab), "--quiet", "--no-install")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if installed != nil {
		t.Fatalf("expected no install under --no-install, but got: %q", *installed)
	}
	if !strings.Contains(stdout, "--no-install set") {
		t.Errorf("expected no-install outcome, got:\n%s", stdout)
	}
}

func TestEditLiveUnchangedDoesNotInstall(t *testing.T) {
	prev := crontabLoader
	crontabLoader = staticLoader(cleanCrontab)
	t.Cleanup(func() { crontabLoader = prev })

	// Editor leaves the dumped crontab untouched -> unchanged -> no install.
	stdout, _, installed, err := runEdit(t, noopEditor, "--quiet")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if installed != nil {
		t.Fatalf("unchanged crontab should not be installed, got: %q", *installed)
	}
	if !strings.Contains(stdout, "No changes made") {
		t.Errorf("expected no-change outcome, got:\n%s", stdout)
	}
}

// --- JSON (non-interactive) -----------------------------------------------

func TestEditJSONReportsErrorWithoutInstalling(t *testing.T) {
	prev := crontabLoader
	crontabLoader = staticLoader("# start\n")
	t.Cleanup(func() { crontabLoader = prev })

	stdout, _, installed, err := runEdit(t, rewriteEditor(errCrontab), "--quiet", "--json")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if installed != nil {
		t.Fatalf("json mode must not install a crontab with errors, got: %q", *installed)
	}

	var res editResultJSON
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout)
	}
	if res.Installed {
		t.Error("installed should be false for an errored crontab in json mode")
	}
	if !res.Changed {
		t.Error("changed should be true")
	}
	if res.Lint.Worst != "error" {
		t.Errorf("expected worst=error, got %q", res.Lint.Worst)
	}
	if res.Lint.OK {
		t.Error("lint.ok should be false with an error finding")
	}
}

func TestEditJSONCleanInstalls(t *testing.T) {
	prev := crontabLoader
	crontabLoader = staticLoader("* * * * * /old\n")
	t.Cleanup(func() { crontabLoader = prev })

	stdout, _, installed, err := runEdit(t, rewriteEditor(cleanCrontab), "--quiet", "--json")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if installed == nil {
		t.Fatal("clean crontab should install in json mode")
	}
	var res editResultJSON
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if !res.Installed || !res.Changed || !res.Lint.OK {
		t.Errorf("expected installed+changed+ok, got %+v", res)
	}
}

// --- ci-level threshold ----------------------------------------------------

func TestEditCILevelErrorSkipsWarningPrompt(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "cron-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("# start\n")
	f.Close()

	// A warning-only crontab with --ci-level error is "clean enough": no prompt,
	// straight to Saved. No stdin needed; if it prompted, ReadString would EOF
	// and abort, which we assert against.
	stdout, _, _, err := runEdit(t, rewriteEditor(warnCrontab), "--quiet", "--ci-level", "error", f.Name())
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if strings.Contains(stdout, "Aborted") {
		t.Errorf("warning under --ci-level error should not prompt/abort, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "Saved.") {
		t.Errorf("expected Saved outcome, got:\n%s", stdout)
	}
}
