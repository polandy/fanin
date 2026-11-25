package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/polandy/fanin/internal/cli"
	"github.com/polandy/fanin/internal/config"
)

const flagJSON = "--json"

func init() {
	register(Command{
		Name:    "config",
		Summary: "validate the config directory or print the effective config of a project (validate, effective)",
		Run:     runConfig,
	})
}

// envSecrets tells whether a named secret is set in the environment; it never returns a value.
type envSecrets struct{}

func (envSecrets) Lookup(name string) (string, bool) { return os.LookupEnv(name) }

func runConfig(_ context.Context, g Globals, out cli.Output, args []string) error {
	if len(args) == 0 {
		return cli.Usagef("config needs a subcommand: validate [DIR] or effective [%s] PROJECT", flagJSON)
	}
	switch args[0] {
	case "validate":
		return configValidate(g, out, args[1:])
	case "effective":
		return configEffective(g, out, args[1:])
	}
	return cli.Usagef("unknown config subcommand %q; use validate or effective", args[0])
}

// configValidate loads the directory and prints one ok line; an invalid config is an error naming file, key
// path and rule.
func configValidate(g Globals, out cli.Output, args []string) error {
	if len(args) > 1 {
		return cli.Usagef("config validate takes at most one directory, got %s", strings.Join(args, " "))
	}
	dir := g.Config
	if len(args) == 1 {
		dir = args[0]
	}
	if dir == "" {
		return cli.Usagef("config validate needs a directory; pass it, or set --config or $%s", envConfig)
	}
	cfg, err := config.Load(dir)
	if err != nil {
		return err
	}
	out.Printf("%s: ok (%d projects)\n", dir, len(cfg.Projects))
	return nil
}

// configEffective prints the effective config of one project with the origin of every value.
func configEffective(g Globals, out cli.Output, args []string) error {
	var asJSON bool
	var names []string
	for _, a := range args {
		switch {
		case a == flagJSON:
			asJSON = true
		case strings.HasPrefix(a, "-"):
			return cli.Usagef("config effective: unknown flag %q; the only flag is %s", a, flagJSON)
		default:
			names = append(names, a)
		}
	}
	if len(names) != 1 {
		return cli.Usagef("config effective takes exactly one project name, got %d", len(names))
	}
	if g.Config == "" {
		return cli.Usagef("config effective needs the config directory; set --config or $%s", envConfig)
	}
	cfg, err := config.Load(g.Config)
	if err != nil {
		return err
	}
	eff, err := cfg.Effective(names[0], envSecrets{})
	if errors.Is(err, config.ErrUnknownProject) {
		return cli.Usagef("%v; known projects: %s", err, strings.Join(cfg.ProjectNames(), ", "))
	}
	if err != nil {
		return fmt.Errorf("config effective: %w", err)
	}
	x, err := cfg.Explain(eff)
	if err != nil {
		return err
	}
	if asJSON {
		return x.WriteJSON(out.Out)
	}
	return x.WriteYAML(out.Out)
}
