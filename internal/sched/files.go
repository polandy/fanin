package sched

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	// SettingsName is the Claude settings file whose status line sends the worker's usage (SCENARIOS AW-14).
	SettingsName   = "settings.json"
	StatusLineType = "command"
	WorkerUsageArg = "worker-usage"
	// DefaultCommand is the fanin CLI the worker's MCP server runs, found on PATH.
	DefaultCommand = "fanin"
	dirPerm        = 0o700
	filePerm       = 0o600
)

// RunPaths are the files Prepare wrote for one run.
type RunPaths struct {
	MCPConfig string
	Settings  string
}

// Files writes each run's MCP config and Claude settings into <Dir>/<run ID>/, the run directory that also takes result.json.
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

type statusLine struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

type claudeSettings struct {
	StatusLine statusLine `json:"statusLine"`
}

// Prepare writes the run's MCP config and settings, readable only by their owner since they hold the run token.
func (f Files) Prepare(_ context.Context, _ config.Project, run model.Run) (RunPaths, error) {
	dir := filepath.Join(f.Dir, string(run.ID))
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return RunPaths{}, fmt.Errorf("sched: create the run directory of %s: %w; make %s writable", run.ID, err,
			f.Dir)
	}
	cmd := f.Command
	if cmd == "" {
		cmd = DefaultCommand
	}
	env := map[string]string{faninmcp.EnvURL: f.DaemonURL, faninmcp.EnvRunID: string(run.ID),
		faninmcp.EnvRunToken: string(run.Token)}
	paths := RunPaths{MCPConfig: filepath.Join(dir, MCPConfigName), Settings: filepath.Join(dir, SettingsName)}
	mcp := mcpConfig{Servers: map[string]mcpServer{MCPServerName: {Command: cmd, Args: []string{WorkerMCPArg},
		Env: env}}}
	if err := writeJSON(paths.MCPConfig, "MCP config", run.ID, mcp); err != nil {
		return RunPaths{}, err
	}
	settings := claudeSettings{StatusLine: statusLine{Type: StatusLineType, Command: fmt.Sprintf("%s=%s %s=%s %s=%s %s %s",
		faninmcp.EnvURL, shellQuote(f.DaemonURL), faninmcp.EnvRunID, shellQuote(string(run.ID)),
		faninmcp.EnvRunToken, shellQuote(string(run.Token)), shellQuote(cmd), WorkerUsageArg)}}
	if err := writeJSON(paths.Settings, "settings", run.ID, settings); err != nil {
		return RunPaths{}, err
	}
	return paths, nil
}

func writeJSON(path, what string, run model.RunID, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("sched: encode the %s of %s: %w", what, run, err)
	}
	if err := atomicfile.Write(path, data, filePerm); err != nil {
		return fmt.Errorf("sched: write the %s of %s: %w", what, run, err)
	}
	return nil
}

// shellQuote wraps s in single quotes for sh, the shell Claude Code runs the status line in.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
