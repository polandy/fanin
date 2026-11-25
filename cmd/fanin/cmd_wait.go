package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/polandy/fanin/internal/api/client"
	"github.com/polandy/fanin/internal/api/wire"
	"github.com/polandy/fanin/internal/cli"
)

func init() {
	register(Command{
		Name:    "wait",
		Summary: "block until the next event and print it: wait [--owner] [--after seq]",
		Run:     runWait,
	})
}

func runWait(ctx context.Context, g Globals, out cli.Output, args []string) error {
	fs := flag.NewFlagSet("wait", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	owner := fs.Bool("owner", false, "wait only for an event that needs the owner")
	after := fs.Int64("after", client.FromNow, "event sequence to wait after (default: only new events)")
	if err := fs.Parse(args); err != nil {
		return cli.Usagef("wait: %v", err)
	}
	if fs.NArg() != 0 || *after < client.FromNow {
		return cli.Usagef("wait takes only --owner and --after <seq ≥ 0>: fanin wait [--owner] [--after seq]")
	}
	c, err := daemonClient(g, "wait")
	if err != nil {
		return err
	}
	ev, err := c.Wait(ctx, *owner, *after)
	if err != nil {
		return fmt.Errorf("wait: %w", err)
	}
	out.Println(eventLine(ev))
	return nil
}

// eventLine renders one event as "seq type project task run: detail", with "-" for an empty field.
func eventLine(ev wire.Event) string {
	return fmt.Sprintf("%d %s %s %s %s: %s", ev.Seq, ev.Type, orNone(ev.Project), orNone(ev.TaskID),
		orNone(ev.RunID), ev.Detail)
}
