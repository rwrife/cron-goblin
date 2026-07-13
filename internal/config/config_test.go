package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile is a tiny helper to drop a file with content under dir.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestFindWalksUp(t *testing.T) {
	root := t.TempDir()
	// Put a .goblinrc at the root, then search from a nested subdir.
	writeFile(t, filepath.Join(root, FileName), "timezone = \"UTC\"\n")
	nested := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := Find(nested)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	want := filepath.Join(root, FileName)
	if got != want {
		t.Fatalf("Find walk-up = %q, want %q", got, want)
	}
}

func TestFindStopsAtNearest(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, FileName), "timezone = \"UTC\"\n")
	nearDir := filepath.Join(root, "a", "b")
	writeFile(t, filepath.Join(nearDir, FileName), "timezone = \"Europe/Paris\"\n")

	start := filepath.Join(nearDir, "c")
	if err := os.MkdirAll(start, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := Find(start)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if want := filepath.Join(nearDir, FileName); got != want {
		t.Fatalf("Find nearest = %q, want %q", got, want)
	}
}

func TestFindNoneReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	got, err := Find(dir)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if got != "" {
		t.Fatalf("Find with no config = %q, want empty", got)
	}
}

func TestLoadParsesKeys(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, FileName), `
timezone = "America/New_York"

[lint]
disable = ["too-frequent", "collision"]
ci = true
`)
	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Timezone != "America/New_York" {
		t.Errorf("Timezone = %q", cfg.Timezone)
	}
	if !cfg.Disabled("too-frequent") || !cfg.Disabled("collision") {
		t.Errorf("Disable not parsed: %v", cfg.Lint.Disable)
	}
	if cfg.Disabled("dead-expression") {
		t.Errorf("dead-expression should not be disabled")
	}
	if !cfg.CIEnabled() {
		t.Errorf("CIEnabled = false, want true")
	}
	if cfg.Path == "" {
		t.Errorf("Path should record the loaded file")
	}
}

func TestLoadMissingIsZeroConfig(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Timezone != "" || cfg.CIEnabled() || len(cfg.Lint.Disable) != 0 || cfg.Path != "" {
		t.Fatalf("missing config should be zero value, got %+v", cfg)
	}
}

func TestLoadCIFalseIsExplicit(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, FileName), "[lint]\nci = false\n")
	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.CIEnabled() {
		t.Errorf("ci = false should not enable CI")
	}
	if cfg.Lint.CI == nil {
		t.Errorf("explicit ci = false should be non-nil (set)")
	}
}

func TestLoadMalformedNamesFileAndLine(t *testing.T) {
	root := t.TempDir()
	// Invalid TOML: unterminated string on line 2.
	writeFile(t, filepath.Join(root, FileName), "timezone = \"UTC\"\nbroken = \n")
	_, err := Load(root)
	if err == nil {
		t.Fatal("expected error on malformed .goblinrc")
	}
	msg := err.Error()
	if !strings.Contains(msg, FileName) {
		t.Errorf("error should name the file, got %q", msg)
	}
	if !strings.Contains(msg, "line") {
		t.Errorf("error should mention a line, got %q", msg)
	}
}

func TestLoadUnknownKeysCollected(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, FileName), "timezone = \"UTC\"\nwidgets = 3\n\n[lint]\nbogus = true\n")
	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load should not hard-fail on unknown keys: %v", err)
	}
	if len(cfg.Unknown) == 0 {
		t.Fatalf("expected unknown keys to be collected")
	}
	joined := strings.Join(cfg.Unknown, ",")
	if !strings.Contains(joined, "widgets") || !strings.Contains(joined, "bogus") {
		t.Errorf("unknown keys = %v, want widgets + lint.bogus", cfg.Unknown)
	}
}
