package main

import (
	"github.com/polandy/fanin/internal/api/client"
	"github.com/polandy/fanin/internal/cli"
)

// daemonClient returns an API client for the named subcommand, or a usage error without a daemon URL.
func daemonClient(g Globals, name string) (*client.Client, error) {
	if g.DaemonURL == "" {
		return nil, cli.Usagef("%s needs the daemon URL; set --daemon-url or $%s", name, envURL)
	}
	return client.New(g.DaemonURL, g.Token, nil), nil
}
