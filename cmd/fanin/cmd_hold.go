package main

import (
	"context"
	"fmt"

	"github.com/polandy/fanin/internal/cli"
)

// holdLift is the only hold action.
const holdLift = "lift"

func init() {
	register(Command{
		Name:    "hold",
		Summary: "lift a hold: hold lift <hold id>",
		Run:     runHold,
	})
}

func runHold(ctx context.Context, g Globals, out cli.Output, args []string) error {
	if len(args) != 2 || args[0] != holdLift {
		return cli.Usagef("hold needs an action and a hold id: fanin hold %s <hold id>", holdLift)
	}
	c, err := daemonClient(g, "hold")
	if err != nil {
		return err
	}
	ack, err := c.LiftHold(ctx, args[1])
	if err != nil {
		return fmt.Errorf("hold lift %s: %w", args[1], err)
	}
	out.Println(args[1] + ": " + ack.Detail)
	return nil
}
