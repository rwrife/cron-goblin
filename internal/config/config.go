// Package config discovers and parses a project-local `.goblinrc` file so a
// repository can pin its default timezone and lint ruleset once, instead of
// every teammate passing flags. Discovery walks up from a start directory to
// the filesystem root, stopping at the nearest `.goblinrc` (TOML).
//
// This package is deliberately persona-free and side-effect-free: it reads and
// parses configuration and reports errors, but it does not print, resolve
// precedence against CLI flags, or touch the environment. The CLI layer
// (cmd/goblin) owns precedence (flag > GOBLIN_TZ > .goblinrc > built-in) and
// all user-facing messaging.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// FileName is the fixed name of the config file discovered by walking up the
// directory tree.
const FileName = ".goblinrc"

// Config is the parsed, typed representation of a `.goblinrc`. A zero Config is
// valid and means "no configured overrides". Path records where the config was
// loaded from (empty when there was none), which callers may surface in errors
// or verbose output.
type Config struct {
	// Timezone is the IANA name (e.g. "America/New_York") used as the default
	// for tz-aware commands when neither a flag nor GOBLIN_TZ is set. Empty
	// means "unset".
	Timezone string `toml:"timezone"`

	// Lint holds lint-specific defaults.
	Lint Lint `toml:"lint"`

	// Path is the absolute path of the file this Config was loaded from, or ""
	// when no config file applied. Not a TOML key.
	Path string `toml:"-"`

	// Unknown collects TOML keys that were present in the file but not
	// recognized. Callers may warn about these without hard-failing. Not a TOML
	// key itself.
	Unknown []string `toml:"-"`
}

// Lint holds the `[lint]` table of a `.goblinrc`.
type Lint struct {
	// Disable lists lint rule codes (e.g. "too-frequent") that this project
	// intentionally opts out of.
	Disable []string `toml:"disable"`

	// CI, when true, makes lint/doctor fail pipelines on warnings by default
	// (equivalent to passing --ci). It is a pointer so callers can tell "unset"
	// from an explicit "ci = false" if they ever need to; nil means unset.
	CI *bool `toml:"ci"`

	// CILevel sets the severity threshold at which CI mode fails: "warning"
	// (fail on warnings or errors, the default) or "error" (fail only on
	// errors). Empty means unset; the CLI layer applies the default.
	CILevel string `toml:"ci_level"`
}

// Disabled reports whether the named rule code is in the Lint.Disable list.
func (c Config) Disabled(rule string) bool {
	for _, r := range c.Lint.Disable {
		if r == rule {
			return true
		}
	}
	return false
}

// CIEnabled reports whether `[lint] ci` was set to true in the config. It is
// false when the key is unset or explicitly false.
func (c Config) CIEnabled() bool {
	return c.Lint.CI != nil && *c.Lint.CI
}

// Find walks up from startDir to the filesystem root and returns the path of
// the nearest `.goblinrc`, or "" (with a nil error) when none is found. An
// empty startDir defaults to the current working directory.
func Find(startDir string) (string, error) {
	dir := startDir
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		dir = wd
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}

	for {
		candidate := filepath.Join(abs, FileName)
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() {
			return candidate, nil
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			// Reached the filesystem root without a hit.
			return "", nil
		}
		abs = parent
	}
}

// Load discovers the nearest `.goblinrc` starting at startDir and parses it. If
// no config file exists, it returns a zero Config (with an empty Path) and a
// nil error — absence of config is not an error. A malformed file yields a
// clear error naming the file (and line, when TOML provides one).
func Load(startDir string) (Config, error) {
	path, err := Find(startDir)
	if err != nil {
		return Config{}, err
	}
	if path == "" {
		return Config{}, nil
	}
	return LoadFile(path)
}

// LoadFile parses a specific `.goblinrc` at path. It is used by Load and is
// exposed for callers (and tests) that already know the exact file. A malformed
// file produces an error that names the file and, when available, the line.
func LoadFile(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}

	var cfg Config
	md, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return Config{}, fmt.Errorf("parsing %s: %w", path, decorateTOMLErr(path, err))
	}
	cfg.Path = path
	cfg.Unknown = undecodedKeys(md)

	if err := cfg.validate(); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// validate performs light semantic checks that the TOML decoder can't express.
// It intentionally does not resolve the timezone against the system zone
// database — that's the CLI's job (via loadLocation), so a config authored for
// another host doesn't fail on a machine missing that zone at load time.
func (c Config) validate() error {
	return nil
}

// undecodedKeys returns the dotted key names present in the file that did not
// map onto a struct field, so callers can warn about typos without failing.
func undecodedKeys(md toml.MetaData) []string {
	keys := md.Undecoded()
	if len(keys) == 0 {
		return nil
	}
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k.String())
	}
	return out
}

// decorateTOMLErr turns a BurntSushi decode error into a message with a line
// number when the library provides positional info, so users can find the
// offending line quickly.
func decorateTOMLErr(path string, err error) error {
	var perr toml.ParseError
	if errors.As(err, &perr) {
		msg := perr.Message
		if msg == "" {
			msg = perr.Error()
		}
		return fmt.Errorf("line %d: %s", perr.Position.Line, msg)
	}
	return err
}
