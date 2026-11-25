// Command fanin is the fanin CLI. Each subcommand registers itself from its own cmd_<name>.go file.
package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"

	"github.com/polandy/fanin/internal/cli"
)

// Environment fallbacks of the global flags.
const (
	envConfig = "FANIN_CONFIG"
	envURL    = "FANIN_URL"
	envToken  = "FANIN_TOKEN"
)

func main() {
	out := cli.Output{Out: os.Stdout, Err: os.Stderr}
	os.Exit(run(context.Background(), defaultRegistry, os.Args[1:], os.Getenv, out))
}

// run parses the global flags, dispatches to the subcommand and returns the exit code.
func run(ctx context.Context, reg *Registry, args []string, getenv func(string) string, out cli.Output) int {
	var g Globals
	fs := flag.NewFlagSet("fanin", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&g.Config, "config", getenv(envConfig), "path to the config file ($"+envConfig+")")
	fs.StringVar(&g.DaemonURL, "daemon-url", getenv(envURL), "daemon base URL ($"+envURL+")")
	fs.StringVar(&g.Token, "token", getenv(envToken), "API token ($"+envToken+")")

	if err := fs.Parse(args); err != nil {
		return fail(reg, out, cli.Usagef("%v", err))
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return fail(reg, out, cli.Usagef("no subcommand given"))
	}
	cmd, ok := reg.Lookup(rest[0])
	if !ok {
		return fail(reg, out, cli.Usagef("unknown subcommand %q", rest[0]))
	}
	if err := cmd.Run(ctx, g, out, rest[1:]); err != nil {
		return fail(reg, out, err)
	}
	return cli.ExitOK
}

// fail reports err on stderr, adds the usage text for a usage error and returns the exit code.
func fail(reg *Registry, out cli.Output, err error) int {
	out.Errorf("%v", err)
	code := cli.ExitCode(err)
	if errors.Is(err, cli.ErrUsage) {
		printUsage(reg, out)
	}
	return code
}

func printUsage(reg *Registry, out cli.Output) {
	_, _ = io.WriteString(out.Err, "usage: fanin [--config FILE] [--daemon-url URL] [--token TOKEN] <command> [args]\n\ncommands:\n")
	for _, c := range reg.Commands() {
		_, _ = io.WriteString(out.Err, "  "+c.Name+"\t"+c.Summary+"\n")
	}
}
