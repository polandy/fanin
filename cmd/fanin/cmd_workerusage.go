package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/polandy/fanin/internal/cli"
	"github.com/polandy/fanin/pkg/faninmcp"
)

// Limits of one status-line call; Claude Code cancels a slow one at its next update anyway.
const (
	maxStatusBytes = 1 << 20
	usageTimeout   = 5 * time.Second
)

func init() {
	register(Command{
		Name: "worker-usage",
		Summary: "Claude Code status line of a fanin worker: send the context usage from stdin for the run whose " +
			"mcp.json is the argument",
		Run: runWorkerUsage,
	})
}

func runWorkerUsage(ctx context.Context, _ Globals, out cli.Output, args []string) error {
	if len(args) != 1 {
		return cli.Usagef("worker-usage takes one argument, the run's mcp.json; fanind puts it in the status line")
	}
	env, err := faninmcp.ReadRunEnv(args[0])
	if err != nil {
		return fmt.Errorf("worker-usage: %w", err)
	}
	run := env.RunID
	c, err := faninmcp.NewClient(env.URL, run, env.Token, nil)
	if err != nil {
		return fmt.Errorf("worker-usage: %w", err)
	}
	if out.In == nil {
		return errors.New("worker-usage: no standard input; run it as Claude Code's statusLine command")
	}
	data, err := io.ReadAll(io.LimitReader(out.In, maxStatusBytes))
	if err != nil {
		return fmt.Errorf("worker-usage: read the status from stdin: %w", err)
	}
	u, err := faninmcp.ParseStatusLine(data)
	if err != nil {
		return fmt.Errorf("worker-usage: %w", err)
	}
	if u != (faninmcp.Usage{}) {
		ctx, cancel := context.WithTimeout(ctx, usageTimeout)
		defer cancel()
		if err := c.Usage(ctx, u); err != nil {
			return fmt.Errorf("worker-usage: send the usage of run %s: %w", run, err)
		}
	}
	out.Printf("fanin %s · context %d%%\n", run, u.ContextPct)
	return nil
}
