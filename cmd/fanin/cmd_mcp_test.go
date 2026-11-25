package main

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/polandy/fanin/internal/api/client"
	"github.com/polandy/fanin/internal/cli"
	"github.com/polandy/fanin/internal/ownermcp"
)

// mcpCall serves the owner server against url and calls one tool; it returns the text and the tool-error flag.
func mcpCall(t *testing.T, url, tool string, args map[string]any) (string, bool) {
	t.Helper()
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := ownermcp.New(client.New(url, statusToken, nil), url, "test").Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", tool, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError
}

// TestOwnerMCP runs the same cases through `fanin answer` and the MCP tool: one daemon route decides both.
func TestOwnerMCP(t *testing.T) {
	cases := []struct {
		name     string
		answer   string
		wantFail bool
		want     string
		sent     int
	}{
		{"OA-1_option_number_is_delivered", "2", false, "a1: option 2: no", 1},
		{"OA-1_invalid_option_is_refused_with_the_options", "go ahead", true, "1) yes, 2) no", 0},
		{"OA-1_option_out_of_range_is_refused_with_the_options", "7", true, "1) yes, 2) no", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/cli", func(t *testing.T) {
			_, del, url := startOwnerDaemon(t)
			code, out, errOut := ownerRun(t, url, "answer", "a1", tc.answer)
			text := out + errOut
			if (code != cli.ExitOK) != tc.wantFail || !strings.Contains(text, tc.want) || len(del.got) != tc.sent {
				t.Errorf("exit %d, output %q, sent %d; want fail=%v with %q, sent %d", code, text, len(del.got), tc.wantFail, tc.want, tc.sent)
			}
		})
		t.Run(tc.name+"/mcp", func(t *testing.T) {
			_, del, url := startOwnerDaemon(t)
			text, isErr := mcpCall(t, url, ownermcp.ToolAnswer, map[string]any{"task": "a1", "answer": tc.answer})
			if isErr != tc.wantFail || !strings.Contains(text, tc.want) || len(del.got) != tc.sent {
				t.Errorf("isError %v, text %q, sent %d; want fail=%v with %q, sent %d", isErr, text, len(del.got), tc.wantFail, tc.want, tc.sent)
			}
		})
	}
	t.Run("OA-3_failed_delivery_is_a_tool_error", func(t *testing.T) {
		_, del, url := startOwnerDaemon(t)
		del.err = errPaneGone
		if text, isErr := mcpCall(t, url, ownermcp.ToolAnswer, map[string]any{"task": "a1", "answer": "1"}); !isErr {
			t.Errorf("text %q, want a tool error", text)
		}
	})
	t.Run("questions_lists_the_open_question", func(t *testing.T) {
		_, _, url := startOwnerDaemon(t)
		text, isErr := mcpCall(t, url, ownermcp.ToolQuestions, map[string]any{})
		if isErr || !strings.Contains(text, "Proceed?") {
			t.Errorf("isError %v, text %q; want the open question", isErr, text)
		}
	})
	t.Run("status_filters_by_project", func(t *testing.T) {
		_, _, url := startOwnerDaemon(t)
		text, isErr := mcpCall(t, url, ownermcp.ToolStatus, map[string]any{"project": projAlpha})
		if isErr || !strings.Contains(text, `"a1"`) {
			t.Errorf("isError %v, text %q; want task a1", isErr, text)
		}
		text, isErr = mcpCall(t, url, ownermcp.ToolStatus, map[string]any{"project": "nope"})
		if !isErr || !strings.Contains(text, "known: "+projAlpha) {
			t.Errorf("isError %v, text %q; want an unknown-project error listing %s", isErr, text, projAlpha)
		}
	})
	t.Run("go_on_a_task_that_is_not_ready_is_a_tool_error", func(t *testing.T) {
		_, _, url := startOwnerDaemon(t)
		if text, isErr := mcpCall(t, url, ownermcp.ToolGo, map[string]any{"task": "a1"}); !isErr {
			t.Errorf("text %q, want a tool error", text)
		}
	})
}

func TestMCPCommandUsage(t *testing.T) {
	if code, _, _ := runArgs(t, defaultRegistry, nil, "mcp", "extra"); code != cli.ExitUsage {
		t.Errorf("extra argument: exit %d, want %d", code, cli.ExitUsage)
	}
	if code, _, _ := runArgs(t, defaultRegistry, nil, "mcp"); code != cli.ExitUsage {
		t.Errorf("no daemon URL: exit %d, want %d", code, cli.ExitUsage)
	}
}
