package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/polandy/fanin/internal/clock"
)

// TestDaemon covers the scenarios about how fanind is started.
func TestDaemon(t *testing.T) {
	t.Run("WD-4_systemd_bare_environment", func(t *testing.T) {
		cfg := writeConfig(t, t.TempDir(), "127.0.0.1:0")
		bare := map[string]string{"PATH": "/usr/bin:/bin"}

		// Only PATH and --config: the config is read, and the missing token is named from it.
		d := deps{getenv: func(k string) string { return bare[k] }, logOut: &bytes.Buffer{},
			clock: clock.NewManual(t0), ready: func(string) { t.Error("ready called without an owner token") }}
		err := run(context.Background(), []string{flagConfig, cfg}, d)
		if !errors.Is(err, errNoOwnerToken) || !strings.Contains(err.Error(), tokenEnv) {
			t.Fatalf("run with only PATH: %v, want %v naming %s from fanin.yaml", err, errNoOwnerToken, tokenEnv)
		}

		// The unit's EnvironmentFile adds the token: the store is opened and the API serves.
		got := startEnv(t, cfg, map[string]string{"PATH": bare["PATH"], tokenEnv: token})
		got.cancel()
		if err := <-got.done; err != nil {
			t.Errorf("run after cancel: %v, want nil", err)
		}
		if _, err := os.Stat(filepath.Join(cfg, "fanin.db")); err != nil {
			t.Errorf("store file: %v, want it created next to the config", err)
		}
	})
}
