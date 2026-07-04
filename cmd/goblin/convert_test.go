package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// runConvert builds a fresh convert command and captures its streams.
func runConvert(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := newConvertCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

func TestConvertHumanOutput(t *testing.T) {
	stdout, stderr, err := runConvert(t, "--quiet", "--from", "quartz", "0 0 9 ? * MON-FRI")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	// The converted cron line must be the very first line of stdout (pipeable).
	first := strings.SplitN(strings.TrimSpace(stdout), "\n", 2)[0]
	if first != "0 9 * * MON-FRI" {
		t.Errorf("first stdout line = %q, want the converted cron expression", first)
	}
	if !strings.Contains(stdout, "# ") {
		t.Errorf("stdout missing English readback comment, got: %q", stdout)
	}
	if strings.TrimSpace(stderr) != "" {
		t.Errorf("--quiet should silence stderr, got: %q", stderr)
	}
}

func TestConvertNumericWeekdayShift(t *testing.T) {
	// Quartz 2-6 (MON-FRI) must shift to standard cron 1-5.
	stdout, _, err := runConvert(t, "--quiet", "--from", "quartz", "0 0 9 ? * 2-6")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	first := strings.SplitN(strings.TrimSpace(stdout), "\n", 2)[0]
	if first != "0 9 * * 1-5" {
		t.Errorf("weekday shift = %q, want %q", first, "0 9 * * 1-5")
	}
}

func TestConvertJoinsBareWords(t *testing.T) {
	// Unquoted multi-field expressions should work by joining args.
	stdout, _, err := runConvert(t, "--quiet", "--from", "quartz", "0", "30", "2", "*", "*", "?")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	first := strings.SplitN(strings.TrimSpace(stdout), "\n", 2)[0]
	if first != "30 2 * * *" {
		t.Errorf("joined-words expression = %q, want %q", first, "30 2 * * *")
	}
}

func TestConvertPersonaOnStderr(t *testing.T) {
	_, stderr, err := runConvert(t, "--from", "quartz", "0 0 12 * * ?")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if strings.TrimSpace(stderr) == "" {
		t.Error("expected goblin persona on stderr without --quiet")
	}
}

func TestConvertJSON(t *testing.T) {
	stdout, _, err := runConvert(t, "--quiet", "--json", "--from", "quartz", "0 0 9 ? * MON-FRI")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	var payload struct {
		From       string   `json:"from"`
		To         string   `json:"to"`
		Source     string   `json:"source"`
		Cron       string   `json:"cron"`
		English    string   `json:"english"`
		NextRuns   []string `json:"next_runs"`
		NeverFires bool     `json:"never_fires"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, stdout)
	}
	if payload.Cron != "0 9 * * MON-FRI" {
		t.Errorf("cron = %q, want %q", payload.Cron, "0 9 * * MON-FRI")
	}
	if payload.From != "quartz" {
		t.Errorf("from = %q, want quartz", payload.From)
	}
	if payload.To != "cron" {
		t.Errorf("to = %q, want cron", payload.To)
	}
	if payload.Source != "0 0 9 ? * MON-FRI" {
		t.Errorf("source = %q", payload.Source)
	}
	if payload.English == "" {
		t.Error("english readback should not be empty")
	}
	if payload.NeverFires {
		t.Error("never_fires should be false")
	}
	if len(payload.NextRuns) != 1 {
		t.Errorf("expected 1 next_run by default, got %d: %v", len(payload.NextRuns), payload.NextRuns)
	}
	for _, r := range payload.NextRuns {
		if _, perr := time.Parse(time.RFC3339, r); perr != nil {
			t.Errorf("next_run %q is not RFC3339: %v", r, perr)
		}
	}
}

func TestConvertCountFlag(t *testing.T) {
	stdout, _, err := runConvert(t, "--quiet", "--json", "-n", "3", "--from", "quartz", "0 0/20 * * * ?")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	var payload struct {
		NextRuns []string `json:"next_runs"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if len(payload.NextRuns) != 3 {
		t.Errorf("expected 3 next_runs with -n 3, got %d", len(payload.NextRuns))
	}
}

func TestConvertRequiresFromFlag(t *testing.T) {
	// Without --from, the required-flag guard should fail.
	if _, _, err := runConvert(t, "0 0 12 * * ?"); err == nil {
		t.Error("expected error when --from is omitted")
	}
}

func TestConvertRequiresAnArg(t *testing.T) {
	if _, _, err := runConvert(t, "--from", "quartz"); err == nil {
		t.Error("expected error with no expression argument")
	}
}

func TestConvertUnknownDialectFails(t *testing.T) {
	_, stderr, err := runConvert(t, "--quiet", "--from", "nonsense", "0 0 12 * * ?")
	if err == nil {
		t.Fatal("expected error for unknown --from dialect")
	}
	if !strings.Contains(stderr, "error:") {
		t.Errorf("expected a diagnostic on stderr, got: %q", stderr)
	}
}

func TestConvertUnsupportedTargetFails(t *testing.T) {
	_, stderr, err := runConvert(t, "--quiet", "--from", "quartz", "--to", "systemd", "0 0 12 * * ?")
	if err == nil {
		t.Fatal("expected error for unsupported --to target")
	}
	if !strings.Contains(stderr, "error:") {
		t.Errorf("expected a diagnostic on stderr, got: %q", stderr)
	}
}

// TestConvertLossyRefusalHasHint verifies the honest-refusal path: a Quartz
// feature standard cron can't express (a specific year) errors out AND prints a
// hint explaining why, so the user isn't left guessing.
func TestConvertLossyRefusalHasHint(t *testing.T) {
	_, stderr, err := runConvert(t, "--from", "quartz", "0 0 12 * * ? 2027")
	if err == nil {
		t.Fatal("expected a lossy-conversion error for a specific year")
	}
	if !strings.Contains(stderr, "error:") {
		t.Errorf("expected an error line on stderr, got: %q", stderr)
	}
	if !strings.Contains(stderr, "hint:") {
		t.Errorf("expected a hint explaining the lossy refusal, got: %q", stderr)
	}
}

func TestConvertSpecialCharRefused(t *testing.T) {
	// The `#` nth-weekday special has no standard-cron equivalent.
	_, stderr, err := runConvert(t, "--quiet", "--from", "quartz", "0 0 12 ? * 6#3")
	if err == nil {
		t.Fatal("expected error for Quartz # special")
	}
	if !strings.Contains(stderr, "day-of-week") {
		t.Errorf("expected the offending field named on stderr, got: %q", stderr)
	}
}

// TestConvertK8sIsCron confirms k8s schedules (already standard cron) pass
// through validated and normalized rather than being rejected.
func TestConvertK8sIsCron(t *testing.T) {
	stdout, _, err := runConvert(t, "--quiet", "--from", "k8s", "*/5   *  * * *")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	first := strings.SplitN(strings.TrimSpace(stdout), "\n", 2)[0]
	if first != "*/5 * * * *" {
		t.Errorf("k8s passthrough = %q, want normalized %q", first, "*/5 * * * *")
	}
}

// TestConvertSystemdHumanOutput exercises the systemd source path end to end:
// a weekday-range OnCalendar expression should land as the first stdout line.
func TestConvertSystemdHumanOutput(t *testing.T) {
	stdout, stderr, err := runConvert(t, "--quiet", "--from", "systemd", "Mon..Fri 09:00")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	first := strings.SplitN(strings.TrimSpace(stdout), "\n", 2)[0]
	if first != "0 9 * * MON,TUE,WED,THU,FRI" {
		t.Errorf("first stdout line = %q, want the converted cron expression", first)
	}
	if strings.TrimSpace(stderr) != "" {
		t.Errorf("--quiet should silence stderr, got: %q", stderr)
	}
}

// TestConvertSystemdShorthand confirms a named OnCalendar shorthand converts.
func TestConvertSystemdShorthand(t *testing.T) {
	stdout, _, err := runConvert(t, "--quiet", "--from", "systemd", "weekly")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	first := strings.SplitN(strings.TrimSpace(stdout), "\n", 2)[0]
	if first != "0 0 * * MON" {
		t.Errorf("weekly shorthand = %q, want %q", first, "0 0 * * MON")
	}
}

// TestConvertSystemdJSON checks the machine-readable path reports the systemd
// source dialect and a usable converted cron line.
func TestConvertSystemdJSON(t *testing.T) {
	stdout, _, err := runConvert(t, "--quiet", "--json", "--from", "systemd", "*-*-01 00:00")
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	var payload struct {
		From string `json:"from"`
		To   string `json:"to"`
		Cron string `json:"cron"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, stdout)
	}
	if payload.From != "systemd" {
		t.Errorf("from = %q, want systemd", payload.From)
	}
	if payload.To != "cron" {
		t.Errorf("to = %q, want cron", payload.To)
	}
	if payload.Cron != "0 0 1 * *" {
		t.Errorf("cron = %q, want %q", payload.Cron, "0 0 1 * *")
	}
}

// TestConvertSystemdLossyRefusalHasHint verifies the honest-refusal path for a
// systemd expression standard cron can't carry (a specific year): it errors AND
// prints the shared lossy hint.
func TestConvertSystemdLossyRefusalHasHint(t *testing.T) {
	_, stderr, err := runConvert(t, "--from", "systemd", "2027-01-01 00:00")
	if err == nil {
		t.Fatal("expected a lossy-conversion error for a specific year")
	}
	if !strings.Contains(stderr, "error:") {
		t.Errorf("expected an error line on stderr, got: %q", stderr)
	}
	if !strings.Contains(stderr, "hint:") {
		t.Errorf("expected a hint explaining the lossy refusal, got: %q", stderr)
	}
}
