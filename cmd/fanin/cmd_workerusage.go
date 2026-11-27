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
		Summary: "Claude Code status line of a fanin worker: send the context usage from stdin for the run in $" +
			faninmcp.EnvRunID,
		Run: runWorkerUsage,
	})
}

func runWorkerUsage(ctx context.Context, g Globals, out cli.Output, args []string) error {
	if len(args) != 0 {
		return cli.Usagef("worker-usage takes no arguments; fanind sets $%s, $%s and $%s",
			faninmcp.EnvURL, faninmcp.EnvRunID, faninmcp.EnvRunToken)
	}
	run := g.Getenv(faninmcp.EnvRunID)
	c, err := faninmcp.NewClient(g.DaemonURL, run, g.Getenv(faninmcp.EnvRunToken), nil)
	if err != nil {
		return cli.Usagef("worker-usage: %v", err)
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
