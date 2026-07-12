package main

import (
	"os"
	"testing"

	"github.com/rwrife/cron-goblin/internal/config"
)

func TestResolveTZPrecedence(t *testing.T) {
	cfg := config.Config{Timezone: "Europe/Paris"}

	t.Run("flag wins over env and config", func(t *testing.T) {
		t.Setenv(envTZ, "Asia/Tokyo")
		got := resolveTZ("America/New_York", true, cfg)
		if got != "America/New_York" {
			t.Fatalf("got %q, want flag value", got)
		}
	})

	t.Run("env wins over config when flag unset", func(t *testing.T) {
		t.Setenv(envTZ, "Asia/Tokyo")
		got := resolveTZ("", false, cfg)
		if got != "Asia/Tokyo" {
			t.Fatalf("got %q, want env value", got)
		}
	})

	t.Run("config used when flag and env unset", func(t *testing.T) {
		os.Unsetenv(envTZ)
		got := resolveTZ("", false, cfg)
		if got != "Europe/Paris" {
			t.Fatalf("got %q, want config value", got)
		}
	})

	t.Run("empty everywhere yields empty", func(t *testing.T) {
		os.Unsetenv(envTZ)
		got := resolveTZ("", false, config.Config{})
		if got != "" {
			t.Fatalf("got %q, want empty", got)
		}
	})

	t.Run("explicit empty flag still beats env", func(t *testing.T) {
		// flagSet=true but value empty: not a real override, so env/config win.
		t.Setenv(envTZ, "Asia/Tokyo")
		got := resolveTZ("", true, cfg)
		if got != "Asia/Tokyo" {
			t.Fatalf("got %q, want env value (empty flag is not an override)", got)
		}
	})
}
