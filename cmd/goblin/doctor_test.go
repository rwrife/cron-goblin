package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// runDoctor builds a fresh doctor command with a stubbed crontab loader and
// captures its streams. The loader is restored after the test. It mirrors
// runLint in lint_test.go.
func runDoctor(t *testing.T, loader func(user string) (string, error), args ...string) (stdout, stderr string, err error) {
	t.Helper()
	prev := crontabLoader
	crontabLoader = loader
	t.Cleanup(func() { crontabLoader = prev })

	cmd := newDoctorCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

// staticLoader returns a loader that always yields the same crontab text.
func staticLoader(content string) func(string) (string, error) {
	return func(string) (string, error) { return content, nil }
}

// doctorSample reuses the same shape as lint_test's sampleCrontab so doctor and
// lint demonstrably agree on the same input.
const doctorSample = `# example
SHELL=/bin/bash
0 3 * * * /bin/backup
0 3 * * * /bin/logrotate
* * * * * /bin/spin
0 0 30 2 * /bin/never
30 6 * * 1-5 /bin/report
`

func TestDoctorHumanReportsAllRules(t *testing.T) {
	stdout, _, err := runDoctor(t, staticLoader(doctorSample), "--quiet")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	for _, want := range []string{"thundering herd", "fires every minute", "never fires", "5 schedule(s)", "<crontab>"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("human output missing %q, got:\n%s", want, stdout)
		}
	}
	if !strings.Contains(stdout, "1 error(s), 2 warning(s)") {
		t.Errorf("summary line wrong, got:\n%s", stdout)
	}
}

func TestDoctorJSONStable(t *testing.T) {
	stdout, _, err := runDoctor(t, staticLoader(doctorSample), "--quiet", "--json")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	var payload struct {
		Source   string `json:"source"`
		Entries  int    `json:"entries"`
		Findings []struct {
			Rule string `json:"rule"`
		} `json:"findings"`
		Counts struct {
			Warning int `json:"warning"`
			Error   int `json:"error"`
		} `json:"counts"`
		Worst string `json:"worst"`
		OK    bool   `json:"ok"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, stdout)
	}
	if payload.Source != "<crontab>" {
		t.Errorf("source = %q, want <crontab>", payload.Source)
	}
	if payload.Entries != 5 {
		t.Errorf("entries = %d, want 5", payload.Entries)
	}
	if payload.Counts.Error != 1 || payload.Counts.Warning != 2 {
		t.Errorf("counts wrong: %+v", payload.Counts)
	}
	if payload.Worst != "error" || payload.OK {
		t.Errorf("worst/ok wrong: worst=%q ok=%v", payload.Worst, payload.OK)
	}
}

func TestDoctorUserChangesSourceLabel(t *testing.T) {
	clean := "0 3 * * * /bin/backup\n"
	stdout, _, err := runDoctor(t, staticLoader(clean), "--quiet", "--json", "--user", "deploy")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !strings.Contains(stdout, `"source": "\u003ccrontab:deploy\u003e"`) {
		t.Errorf("expected user-scoped source label, got:\n%s", stdout)
	}
}

func TestDoctorNoCrontabIsCleanExit(t *testing.T) {
	loader := func(string) (string, error) { return "", errNoCrontab }
	// Even with --ci, "no crontab" is not a failure: exit zero.
	stdout, _, err := runDoctor(t, loader, "--quiet", "--ci")
	if err != nil {
		t.Fatalf("no crontab should exit zero even with --ci, got: %v", err)
	}
	if !strings.Contains(stdout, "No crontab installed") {
		t.Errorf("expected calm no-crontab message, got:\n%s", stdout)
	}
}

func TestDoctorCIExitsNonZeroOnFindings(t *testing.T) {
	_, _, err := runDoctor(t, staticLoader(doctorSample), "--quiet", "--ci")
	if err == nil {
		t.Fatal("--ci with warnings/errors should return an error (non-zero exit)")
	}
}

func TestDoctorCICleanExitsZero(t *testing.T) {
	clean := "0 3 * * * /bin/backup\n30 6 * * 1-5 /bin/report\n"
	stdout, _, err := runDoctor(t, staticLoader(clean), "--quiet", "--ci")
	if err != nil {
		t.Fatalf("--ci on clean crontab should succeed, got: %v", err)
	}
	if !strings.Contains(stdout, "No problems found") {
		t.Errorf("expected clean message, got:\n%s", stdout)
	}
}

func TestDoctorLoaderErrorSurfaces(t *testing.T) {
	loader := func(string) (string, error) {
		return "", errCrontabBoom
	}
	_, stderr, err := runDoctor(t, loader, "--quiet")
	if err == nil {
		t.Fatal("expected loader error to propagate")
	}
	if !strings.Contains(stderr, "error:") || !strings.Contains(stderr, "boom") {
		t.Errorf("expected error surfaced on stderr, got: %q", stderr)
	}
}

func TestDoctorPersonaOnStderrByDefault(t *testing.T) {
	stdout, stderr, err := runDoctor(t, staticLoader(doctorSample)) // no --quiet
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if strings.TrimSpace(stderr) == "" {
		t.Error("expected goblin persona on stderr without --quiet")
	}
	if strings.Contains(stdout, "I live in your crontab and I have opinions") {
		t.Error("persona leaked into stdout")
	}
}

// errCrontabBoom is a sentinel used by TestDoctorLoaderErrorSurfaces.
var errCrontabBoom = errBoom("boom")

// errBoom is a tiny error type so the test loader can return a non-errNoCrontab
// failure without pulling in extra deps.
type errBoom string

func (e errBoom) Error() string { return string(e) }

// TestDoctorNoCrontabMessageDetection guards the stderr classification used by
// the real loader, since that string match is what separates "clean/empty"
// from "broken".
func TestDoctorNoCrontabMessageDetection(t *testing.T) {
	cases := map[string]bool{
		"no crontab for ryan":          true,
		"no crontab for deploy\n":      true,
		"NO CRONTAB FOR ROOT":          true,
		"crontab: command not found":   false,
		"must be privileged to use -u": false,
		"":                             false,
	}
	for msg, want := range cases {
		if got := isNoCrontabMessage(msg); got != want {
			t.Errorf("isNoCrontabMessage(%q) = %v, want %v", msg, got, want)
		}
	}
}
