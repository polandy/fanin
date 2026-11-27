package faninmcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// MCPServerName is the fanin server's key in a run's MCP config.
const MCPServerName = "fanin"

// ErrMCPConfig is a run's MCP config that cannot be read or has no fanin server.
var ErrMCPConfig = errors.New("faninmcp: unreadable MCP config")

// MCPServer is one server of Claude Code's MCP config.
type MCPServer struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

// MCPConfig is the MCP config fanind writes for each run.
type MCPConfig struct {
	Servers map[string]MCPServer `json:"mcpServers"`
}

// RunEnv is the daemon URL, run ID and run token a run's MCP config gives the fanin server.
type RunEnv struct {
	URL   string
	RunID string
	Token string
}

// ReadRunEnv reads the fanin server's environment from the MCP config at path.
func ReadRunEnv(path string) (RunEnv, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return RunEnv{}, fmt.Errorf("%w: %w; pass the run's mcp.json that fanind wrote", ErrMCPConfig, err)
	}
	var cfg MCPConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return RunEnv{}, fmt.Errorf("%w: %s: %w; pass the run's mcp.json that fanind wrote", ErrMCPConfig, path, err)
	}
	s, ok := cfg.Servers[MCPServerName]
	if !ok {
		return RunEnv{}, fmt.Errorf("%w: %s has no %q server; pass the run's mcp.json that fanind wrote",
			ErrMCPConfig, path, MCPServerName)
	}
	return RunEnv{URL: s.Env[EnvURL], RunID: s.Env[EnvRunID], Token: s.Env[EnvRunToken]}, nil
}
