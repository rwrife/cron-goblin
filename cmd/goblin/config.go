// config.go wires the project-local `.goblinrc` (see internal/config) into the
// CLI. It owns the precedence rules the config package deliberately stays out
// of:
//
//	explicit CLI flag  >  environment (GOBLIN_TZ)  >  .goblinrc  >  built-in default
//
// Discovery is opt-out via the persistent --no-config flag (for reproducible
// CI) and is performed lazily so commands that don't care about config pay
// nothing. All persona/warning output lives here, not in internal/config.
package main

import (
	"fmt"
	"os"
	"sync"

	"github.com/rwrife/cron-goblin/internal/config"
	"github.com/spf13/cobra"
)

// envTZ is the environment variable that sits between an explicit --tz flag and
// the .goblinrc timezone in the precedence chain.
const envTZ = "GOBLIN_TZ"

// configState carries the resolved project config plus the --no-config toggle.
// One instance is attached to the root command and shared with subcommands via
// loadConfig, so discovery happens at most once per process.
type configState struct {
	noConfig bool

	once sync.Once
	cfg  config.Config
	err  error
}

// rootConfig is the process-wide config state, populated by newRootCmd. It's a
// package var (rather than threaded through every constructor) to keep the
// subcommand signatures unchanged while still centralizing discovery.
var rootConfig = &configState{}

// load discovers and parses the nearest `.goblinrc` exactly once. With
// --no-config it short-circuits to a zero Config so CI runs are reproducible.
func (s *configState) load() (config.Config, error) {
	if s.noConfig {
		return config.Config{}, nil
	}
	s.once.Do(func() {
		s.cfg, s.err = config.Load("")
	})
	return s.cfg, s.err
}

// loadConfig resolves the project config for a command, printing a clear error
// (naming the file + line) on a malformed `.goblinrc`. On success it warns —
// once, on stderr, unless --quiet — about any unknown keys, then returns the
// config. A missing config yields a zero Config and no output.
func loadConfig(cmd *cobra.Command, quiet bool) (config.Config, error) {
	cfg, err := rootConfig.load()
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "error: bad %s: %v\n", config.FileName, err)
		return config.Config{}, err
	}
	if !quiet {
		for _, k := range cfg.Unknown {
			fmt.Fprintf(cmd.ErrOrStderr(),
				"warning: %s: unknown key %q (ignored)\n", config.FileName, k)
		}
	}
	return cfg, nil
}

// resolveTZ applies the timezone precedence chain and returns the IANA name to
// use (possibly ""). flagTZ is the raw --tz value; flagSet reports whether the
// user actually passed --tz (so an explicit empty flag still wins over config).
func resolveTZ(flagTZ string, flagSet bool, cfg config.Config) string {
	if flagSet && flagTZ != "" {
		return flagTZ
	}
	if env := os.Getenv(envTZ); env != "" {
		return env
	}
	return cfg.Timezone
}
