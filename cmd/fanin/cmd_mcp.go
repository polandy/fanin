package main

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/polandy/fanin/internal/cli"
	"github.com/polandy/fanin/internal/ownermcp"
	"github.com/polandy/fanin/internal/version"
)

func init() {
	register(Command{
		Name:    "mcp",
		Summary: "serve the owner MCP tools over stdio: answer questions and release tasks without a shell",
		Run:     runMCP,
	})
}

func runMCP(ctx context.Context, g Globals, _ cli.Output, args []string) error {
	if len(args) != 0 {
		return cli.Usagef("mcp takes no arguments: fanin mcp")
	}
	c, err := daemonClient(g, "mcp")
	if err != nil {
		return err
	}
	srv := ownermcp.New(c, g.DaemonURL, version.Version)
	if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil {
		return fmt.Errorf("mcp: serve on stdio: %w", err)
	}
	return nil
}
