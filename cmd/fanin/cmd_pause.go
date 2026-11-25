package main

import (
	"context"
	"fmt"

	"github.com/polandy/fanin/internal/api/client"
	"github.com/polandy/fanin/internal/api/wire"
	"github.com/polandy/fanin/internal/cli"
)

func init() {
	register(Command{
		Name:    "pause",
		Summary: "stop a project before its next worker: pause <project>",
		Run: func(ctx context.Context, g Globals, out cli.Output, args []string) error {
			return runProjectCommand(ctx, g, out, args, "pause", (*client.Client).Pause)
		},
	})
	register(Command{
		Name:    "resume",
		Summary: "clear the pause of a project: resume <project>",
		Run: func(ctx context.Context, g Globals, out cli.Output, args []string) error {
			return runProjectCommand(ctx, g, out, args, "resume", (*client.Client).Resume)
		},
	})
}

func runProjectCommand(ctx context.Context, g Globals, out cli.Output, args []string, name string,
	call func(*client.Client, context.Context, string) (wire.Ack, error)) error {
	if len(args) != 1 {
		return cli.Usagef("%s needs exactly one project: fanin %s <project>", name, name)
	}
	c, err := daemonClient(g, name)
	if err != nil {
		return err
	}
	ack, err := call(c, ctx, args[0])
	if err != nil {
		return fmt.Errorf("%s %s: %w", name, args[0], err)
	}
	out.Println(args[0] + ": " + ack.Detail)
	return nil
}
