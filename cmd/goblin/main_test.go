package main

import (
	"bytes"
	"strings"
	"testing"
)

func runRoot(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := newRootCmd("9.9.9-test")
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

func TestRootGreetsAndReportsVersion(t *testing.T) {
	stdout, stderr, err := runRoot(t)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !strings.Contains(stdout, "9.9.9-test") {
		t.Errorf("stdout missing version, got: %q", stdout)
	}
	if strings.TrimSpace(stderr) == "" {
		t.Error("expected a grumpy greeting on stderr, got nothing")
	}
}

func TestRootQuietSilencesPersona(t *testing.T) {
	_, stderr, err := runRoot(t, "--quiet")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if strings.TrimSpace(stderr) != "" {
		t.Errorf("--quiet should silence stderr, got: %q", stderr)
	}
}

func TestRootVersionFlag(t *testing.T) {
	stdout, _, err := runRoot(t, "--version")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !strings.Contains(stdout, "cron-goblin 9.9.9-test") {
		t.Errorf("--version output unexpected: %q", stdout)
	}
}
