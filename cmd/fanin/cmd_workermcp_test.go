package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/polandy/fanin/internal/cli"
	"github.com/polandy/fanin/pkg/faninmcp"
)

func workerGlobals(env map[string]string) Globals {
	return Globals{DaemonURL: "http://127.0.0.1:1", Getenv: func(k string) string { return env[k] }}
}

func TestWorkerMCP(t *testing.T) {
	full := map[string]string{faninmcp.EnvRunID: "r1", faninmcp.EnvRunToken: "run-secret"}
	t.Run("builds_with_the_run_environment", func(t *testing.T) {
		if _, err := workerServer(workerGlobals(full)); err != nil {
			t.Errorf("workerServer: %v", err)
		}
	})
	for _, missing := range []string{faninmcp.EnvRunID, faninmcp.EnvRunToken} {
		t.Run("usage_error_without_"+missing, func(t *testing.T) {
			env := map[string]string{}
			for k, v := range full {
				if k != missing {
					env[k] = v
				}
			}
			_, err := workerServer(workerGlobals(env))
			if !errors.Is(err, cli.ErrUsage) || !strings.Contains(err.Error(), missing) {
				t.Errorf("workerServer = %v, want a usage error naming %s", err, missing)
			}
		})
	}
	t.Run("usage_error_with_arguments", func(t *testing.T) {
		err := runWorkerMCP(context.Background(), workerGlobals(full), cli.Output{}, []string{"extra"})
		if !errors.Is(err, cli.ErrUsage) {
			t.Errorf("runWorkerMCP = %v, want a usage error", err)
		}
	})
}
