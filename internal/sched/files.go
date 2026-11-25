package sched

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/polandy/fanin/internal/atomicfile"
	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/pkg/faninmcp"
)

// The run directory layout and the worker's MCP server entry.
const (
	MCPConfigName = "mcp.json"
	MCPServerName = "fanin"
	WorkerMCPArg  = "worker-mcp"
	// DefaultCommand is the fanin CLI the worker's MCP server runs, found on PATH.
	DefaultCommand = "fanin"
	dirPerm        = 0o700
	filePerm       = 0o600
)

// Files writes each run's MCP config into <Dir>/<run ID>/, the run directory that also takes result.json.
type Files struct {
	Dir       string
	DaemonURL string
	// Command is the fanin CLI; empty means DefaultCommand.
	Command string
}

type mcpServer struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

type mcpConfig struct {
	Servers map[string]mcpServer `json:"mcpServers"`
}

// Prepare writes the run's MCP config, readable only by its owner since it holds the run token.
func (f Files) Prepare(_ context.Context, _ config.Project, run model.Run) (string, error) {
	dir := filepath.Join(f.Dir, string(run.ID))
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return "", fmt.Errorf("sched: create the run directory of %s: %w; make %s writable", run.ID, err, f.Dir)
	}
	cmd := f.Command
	if cmd == "" {
		cmd = DefaultCommand
	}
	data, err := json.MarshalIndent(mcpConfig{Servers: map[string]mcpServer{MCPServerName: {
		Command: cmd, Args: []string{WorkerMCPArg},
		Env: map[string]string{faninmcp.EnvURL: f.DaemonURL, faninmcp.EnvRunID: string(run.ID),
			faninmcp.EnvRunToken: string(run.Token)},
	}}}, "", "  ")
	if err != nil {
		return "", fmt.Errorf("sched: encode the MCP config of %s: %w", run.ID, err)
	}
	path := filepath.Join(dir, MCPConfigName)
	if err := atomicfile.Write(path, data, filePerm); err != nil {
		return "", fmt.Errorf("sched: write the MCP config of %s: %w", run.ID, err)
	}
	return path, nil
}
