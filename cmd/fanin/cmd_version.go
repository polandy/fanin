package main

import (
	"context"

	"github.com/polandy/fanin/internal/cli"
	"github.com/polandy/fanin/internal/version"
)

func init() {
	register(Command{
		Name:    "version",
		Summary: "print the fanin version",
		Run: func(_ context.Context, _ Globals, out cli.Output, args []string) error {
			if len(args) > 0 {
				return cli.Usagef("version takes no arguments, got %q", args[0])
			}
			out.Println(version.String("fanin"))
			return nil
		},
	})
}
