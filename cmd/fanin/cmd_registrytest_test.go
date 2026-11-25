package main

import (
	"context"
	"testing"

	"github.com/polandy/fanin/internal/cli"
)

// A subcommand lives in its own file and registers itself in init(), without touching main.go.
func init() {
	register(Command{
		Name:    "registrytest",
		Summary: "test-only subcommand",
		Run: func(_ context.Context, _ Globals, out cli.Output, args []string) error {
			out.Printf("registrytest %d\n", len(args))
			return nil
		},
	})
}

func TestSubcommandFromOwnFileRunsThroughDispatch(t *testing.T) {
	code, stdout, stderr := runArgs(t, defaultRegistry, nil, "registrytest", "a", "b")
	if code != cli.ExitOK || stdout != "registrytest 2\n" || stderr != "" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}
