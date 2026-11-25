package main

import (
	"context"
	"fmt"
	"sort"

	"github.com/polandy/fanin/internal/cli"
)

// Globals are the flags every subcommand shares, after env fallback.
type Globals struct {
	Config    string
	DaemonURL string
	Token     string
	// Getenv reads the environment; commands with their own variables use it.
	Getenv func(string) string
}

// Command is one fanin subcommand.
type Command struct {
	Name    string
	Summary string
	Run     func(ctx context.Context, g Globals, out cli.Output, args []string) error
}

// Registry maps subcommand names to commands.
type Registry struct {
	cmds map[string]Command
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{cmds: map[string]Command{}}
}

// Register adds c and fails on an empty or duplicate name.
func (r *Registry) Register(c Command) error {
	if c.Name == "" || c.Run == nil {
		return fmt.Errorf("register subcommand: name %q and Run are required", c.Name)
	}
	if _, dup := r.cmds[c.Name]; dup {
		return fmt.Errorf("register subcommand %q: already registered; pick a unique name", c.Name)
	}
	r.cmds[c.Name] = c
	return nil
}

// Lookup returns the command with the given name.
func (r *Registry) Lookup(name string) (Command, bool) {
	c, ok := r.cmds[name]
	return c, ok
}

// Commands returns all commands sorted by name.
func (r *Registry) Commands() []Command {
	out := make([]Command, 0, len(r.cmds))
	for _, c := range r.cmds {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// defaultRegistry holds the subcommands registered from the cmd_<name>.go files.
var defaultRegistry = NewRegistry()

// register is called from init() in each cmd_<name>.go file.
func register(c Command) {
	if err := defaultRegistry.Register(c); err != nil {
		panic(err)
	}
}
