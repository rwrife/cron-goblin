package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rwrife/cron-goblin/internal/coverage"
)

func runCoverage(t *testing.T, src string, args ...string) (string, string, error) {
	t.Helper()
	cmd := newRootCmd("test")
	var out, stderr bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	cmd.SetIn(strings.NewReader(src))
	cmd.SetArgs(append([]string{"--no-config", "coverage"}, args...))
	err := cmd.Execute()
	return out.String(), stderr.String(), err
}

func TestCoverageCLIJSONAndExit(t *testing.T) {
	for _, tc := range []struct {
		name, src, status string
		wantErr           bool
	}{
		{"covered", "*/15 * * * * job\n", "covered", false},
		{"uncovered", "", "under-covered", true},
		{"redundant", "*/15 * * * * a\n*/15 * * * * b\n*/15 * * * * c\n", "over-covered", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, stderr, err := runCoverage(t, tc.src, "-", "--want", "every 15 minutes", "--tz", "UTC", "--json")
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v; stderr = %s", err, stderr)
			}
			var rep coverage.Report
			if err := json.Unmarshal([]byte(out), &rep); err != nil {
				t.Fatalf("invalid JSON: %v\n%s", err, out)
			}
			if rep.Status != tc.status || rep.Expected != 7*24*4 || rep.To.Sub(rep.From) != coverage.DefaultWindow {
				t.Fatalf("unexpected report: %+v", rep)
			}
			if rep.Gaps == nil || rep.Redundancies == nil {
				t.Fatal("JSON arrays must not be null")
			}
			if tc.wantErr && (len(rep.Gaps) != rep.Expected || !strings.Contains(stderr, "uncovered")) {
				t.Fatal("missing gap report or diagnostic")
			}
			if tc.status == "over-covered" {
				if len(rep.Redundancies) != rep.Expected || rep.Redundancies[0].Count != 3 {
					t.Fatal("missing triple redundancy")
				}
			}
		})
	}
}

func TestCoverageCLIFileAndHuman(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crontab")
	if err := os.WriteFile(path, []byte("* * * * * job\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out, stderr, err := runCoverage(t, "", path, "--want", "0 9 * * *", "--window", "30d", "--tz", "UTC", "--quiet")
	if err != nil || stderr != "" || !strings.Contains(out, "covered (30/30") {
		t.Fatalf("out = %s; stderr = %s; error = %v", out, stderr, err)
	}
	out, _, err = runCoverage(t, "", "--want", "* * * * *", "--window", "2m", "--quiet")
	if err == nil || !strings.Contains(out, "under-covered") || !strings.Contains(out, "Gap:") {
		t.Fatalf("missing human gaps: %s (%v)", out, err)
	}
	out, _, err = runCoverage(t, "* * * * * a\n* * * * * b\n", "--want", "* * * * *", "--window", "2m", "--quiet")
	if err != nil || !strings.Contains(out, "over-covered") || !strings.Contains(out, "lines [1 2]") {
		t.Fatalf("missing human redundancies: %s (%v)", out, err)
	}
	out, _, err = runCoverage(t, "", "--want", "0 0 30 2 *", "--quiet")
	if err != nil || !strings.Contains(out, "No desired fires") {
		t.Fatalf("missing no-target notice: %s (%v)", out, err)
	}
}

func TestCoverageCLIInvalidInputs(t *testing.T) {
	for _, args := range [][]string{
		{"--want", "not a cadence"},
		{"--want", "* * * * *", "--tz", "Not/AZone"},
		{"--want", "* * * * *", "--window", "0d"},
		{"--want", "* * * * *", "--window", "-1h"},
		{"--want", "* * * * *", "--window", "invalid"},
		{"--want", "* * * * *", "--window", "99999999999999999999999d"},
		{"--want", "* * * * *", "--window", "106752d"},
		{"--want", "* * * * *", filepath.Join(t.TempDir(), "missing")},
		{},
	} {
		out, _, err := runCoverage(t, "", args...)
		if err == nil || out != "" {
			t.Errorf("args %v: out = %q; error = %v", args, out, err)
		}
	}
	out, stderr, err := runCoverage(t, "not a crontab\n", "--want", "* * * * *", "--json")
	if err == nil || out != "" || !strings.Contains(stderr, "line 1") {
		t.Fatalf("invalid job silently accepted: %s %s %v", out, stderr, err)
	}
}

func TestCoverageCLIWindowDuration(t *testing.T) {
	out, _, err := runCoverage(t, "* * * * * job", "--want", "* * * * *", "--window", "90m", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rep coverage.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Expected != 90 || rep.To.Sub(rep.From) != 90*time.Minute {
		t.Fatalf("wrong window: %+v", rep)
	}
}
