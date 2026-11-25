package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/polandy/fanin/internal/cli"
)

func init() {
	register(Command{
		Name:    "answer",
		Summary: "answer the open question of a task: answer <task> <option number or text>",
		Run:     runAnswer,
	})
}

func runAnswer(ctx context.Context, g Globals, out cli.Output, args []string) error {
	if len(args) < 2 {
		return cli.Usagef("answer needs a task and an answer: fanin answer <task> <option number or text>")
	}
	c, err := daemonClient(g, "answer")
	if err != nil {
		return err
	}
	ack, err := c.Answer(ctx, args[0], strings.Join(args[1:], " "))
	if err != nil {
		return fmt.Errorf("answer %s: %w", args[0], err)
	}
	out.Println(args[0] + ": " + ack.Detail)
	return nil
}
