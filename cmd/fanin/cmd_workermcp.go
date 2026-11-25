package main

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/polandy/fanin/internal/cli"
	"github.com/polandy/fanin/internal/version"
	"github.com/polandy/fanin/pkg/faninmcp"
)

func init() {
	register(Command{
		Name: "worker-mcp",
		Summary: "serve the worker MCP tools fanin_report and fanin_progress over stdio for the run in $" +
			faninmcp.EnvRunID,
		Run: runWorkerMCP,
	})
}

func runWorkerMCP(ctx context.Context, g Globals, _ cli.Output, args []string) error {
	if len(args) != 0 {
		return cli.Usagef("worker-mcp takes no arguments; fanind sets $%s, $%s and $%s",
			faninmcp.EnvURL, faninmcp.EnvRunID, faninmcp.EnvRunToken)
	}
	srv, err := workerServer(g)
	if err != nil {
		return err
	}
	if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil {
		return fmt.Errorf("worker-mcp: serve on stdio: %w", err)
	}
	return nil
}

// workerServer builds the worker server from the daemon URL and the run's environment.
func workerServer(g Globals) (*mcp.Server, error) {
	c, err := faninmcp.NewClient(g.DaemonURL, g.Getenv(faninmcp.EnvRunID), g.Getenv(faninmcp.EnvRunToken), nil)
	if err != nil {
		return nil, cli.Usagef("worker-mcp: %v", err)
	}
	srv, err := faninmcp.NewServer(c, version.Version)
	if err != nil {
		return nil, fmt.Errorf("worker-mcp: %w", err)
	}
	return srv, nil
}
