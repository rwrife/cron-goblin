package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// runNarrate builds a fresh narrate command and captures its streams.
func runNarrate(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := newNarrateCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

func TestNarrateSingleHuman(t *testing.T) {
	stdout, _, err := runNarrate(t, "30 18 * * 1-5")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(stdout), "This job runs ") {
		t.Errorf("expected changelog prose, got %q", stdout)
	}
	if !strings.Contains(stdout, "18:30") {
		t.Errorf("expected the time in the sentence, got %q", stdout)
	}
}

func TestNarrateChangeHuman(t *testing.T) {
	stdout, _, err := runNarrate(t, "--from", "0 * * * *", "--to", "30 18 * * 1-5")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !strings.Contains(stdout, "instead of") {
		t.Errorf("expected change narration, got %q", stdout)
	}
}

func TestNarrateJSON(t *testing.T) {
	stdout, _, err := runNarrate(t, "--json", "0 9 * * *")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	var payload struct {
		Sentence   string `json:"sentence"`
		Expression string `json:"expression"`
		Change     bool   `json:"change"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("invalid JSON %q: %v", stdout, err)
	}
	if payload.Sentence == "" {
		t.Error("expected a sentence in JSON")
	}
	if payload.Expression != "0 9 * * *" {
		t.Errorf("expected echoed expression, got %q", payload.Expression)
	}
	if payload.Change {
		t.Error("single mode should report change=false")
	}
}

func TestNarrateChangeJSON(t *testing.T) {
	stdout, _, err := runNarrate(t, "--json", "--from", "0 9 * * *", "--to", "0 8 * * *")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	var payload struct {
		From   string `json:"from"`
		To     string `json:"to"`
		Change bool   `json:"change"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("invalid JSON %q: %v", stdout, err)
	}
	if payload.From != "0 9 * * *" || payload.To != "0 8 * * *" {
		t.Errorf("expected from/to echoed, got %+v", payload)
	}
	if !payload.Change {
		t.Error("change mode should report change=true")
	}
}

func TestNarrateFromWithoutTo(t *testing.T) {
	_, _, err := runNarrate(t, "--from", "0 9 * * *")
	if err == nil {
		t.Error("expected error when --from used without --to")
	}
}

func TestNarrateExprPlusFromRejected(t *testing.T) {
	_, _, err := runNarrate(t, "--from", "0 9 * * *", "--to", "0 8 * * *", "0 9 * * *")
	if err == nil {
		t.Error("expected error when mixing positional expr with --from/--to")
	}
}

func TestNarrateBadExpr(t *testing.T) {
	_, stderr, err := runNarrate(t, "--quiet", "not a cron")
	if err == nil {
		t.Error("expected parse error")
	}
	if !strings.Contains(stderr, "error:") {
		t.Errorf("expected diagnostic on stderr, got %q", stderr)
	}
}
