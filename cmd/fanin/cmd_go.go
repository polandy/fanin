package main

import (
	"context"
	"fmt"

	"github.com/polandy/fanin/internal/cli"
)

func init() {
	register(Command{
		Name:    "go",
		Summary: "release a ready task for merge: go <task>",
		Run:     runGo,
	})
}

func runGo(ctx context.Context, g Globals, out cli.Output, args []string) error {
	if len(args) != 1 {
		return cli.Usagef("go needs exactly one task: fanin go <task>")
	}
	c, err := daemonClient(g, "go")
	if err != nil {
		return err
	}
	ack, err := c.Go(ctx, args[0])
	if err != nil {
		return fmt.Errorf("go %s: %w", args[0], err)
	}
	out.Println(args[0] + ": " + ack.Detail)
	return nil
}
