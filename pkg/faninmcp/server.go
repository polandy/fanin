package faninmcp

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tool names of the worker server.
const (
	ToolReport   = "fanin_report"
	ToolProgress = "fanin_progress"
)

// ServerName is the implementation name the worker server announces.
const ServerName = "fanin-worker"

const kindProperty = "kind"

// NewServer returns the worker MCP server whose tools forward to c; a failed call is a tool error, never a success.
func NewServer(c *Client, version string) (*mcp.Server, error) {
	reportSchema, err := ReportSchema()
	if err != nil {
		return nil, err
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: ServerName, Version: version}, nil)
	mcp.AddTool(srv, &mcp.Tool{
		Name: ToolReport,
		Description: "Report this run's result to fanin once the work is done or cannot go on. Each kind needs one " +
			"field: done_pr a pr number, pushed and committed a branch, issue an issue number, nothing and " +
			"blocked a text. Report again after a fix round; the newest report counts.",
		InputSchema: reportSchema,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in Report) (*mcp.CallToolResult, any, error) {
		if err := c.Report(ctx, in); err != nil {
			return fail(ToolReport, c.BaseURL(), err), nil, nil
		}
		return text("reported " + string(in.Kind)), nil, nil
	})
	mcp.AddTool(srv, &mcp.Tool{
		Name: ToolProgress,
		Description: "Tell fanin the run is still working, with a short note. Call it only while you wait on a long " +
			"background job (a build, a CI run, a review); a run with no heartbeat or result counts as stalled. Do not " +
			"call it while you are working in the foreground.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in Progress) (*mcp.CallToolResult, any, error) {
		if err := c.Progress(ctx, in); err != nil {
			return fail(ToolProgress, c.BaseURL(), err), nil, nil
		}
		return text("progress noted"), nil, nil
	})
	return srv, nil
}

// fail names the tool and the cause; a connection failure also names the daemon URL.
func fail(tool, baseURL string, err error) *mcp.CallToolResult {
	var apiErr *Error
	msg := fmt.Sprintf("%s: %v", tool, err)
	if !errors.As(err, &apiErr) {
		msg = fmt.Sprintf("%s: cannot reach fanind at %s: %v; check $%s or write %s in the run directory",
			tool, baseURL, err, EnvURL, ResultFileName)
	}
	res := text(msg)
	res.IsError = true
	return res
}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

// ReportSchema returns the JSON Schema of a Report, with the kinds as an enum.
func ReportSchema() (*jsonschema.Schema, error) { return schemaWithKinds[Report]() }

// ResultFileSchema returns the JSON Schema of result.json, with the kinds as an enum.
func ResultFileSchema() (*jsonschema.Schema, error) { return schemaWithKinds[ResultFile]() }

func schemaWithKinds[T any]() (*jsonschema.Schema, error) {
	s, err := jsonschema.For[T](nil)
	if err != nil {
		return nil, fmt.Errorf("faninmcp: schema of %T: %w", *new(T), err)
	}
	kind, ok := s.Properties[kindProperty]
	if !ok {
		return nil, fmt.Errorf("faninmcp: schema of %T has no %q property", *new(T), kindProperty)
	}
	for _, k := range Kinds {
		kind.Enum = append(kind.Enum, string(k))
	}
	return s, nil
}
