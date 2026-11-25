package ownermcp

import (
	"context"
	"encoding/json"
	"flag"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/polandy/fanin/internal/api/client"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// connect serves New(c) in memory and returns a connected client session.
func connect(t *testing.T, c *client.Client, url string) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := New(c, url, "test").Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func TestToolSchemasGolden(t *testing.T) {
	cs := connect(t, client.New("http://127.0.0.1:1", "", nil), "http://127.0.0.1:1")
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	got, err := json.MarshalIndent(res.Tools, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", "tools.golden.json")
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden: %v; run go test ./internal/ownermcp -update", err)
	}
	if string(got) != string(want) {
		t.Errorf("tool schemas differ from %s; run go test ./internal/ownermcp -update and review the diff\ngot:\n%s", path, got)
	}
}

func TestOwnerMCPConnectionError(t *testing.T) {
	srv := httptest.NewServer(nil)
	url := srv.URL
	srv.Close()
	cs := connect(t, client.New(url, "tok", nil), url)
	for name, args := range map[string]map[string]any{
		ToolQuestions: {},
		ToolAnswer:    {"task": "a1", "answer": "1"},
		ToolGo:        {"task": "a1"},
		ToolStatus:    {},
	} {
		t.Run(name, func(t *testing.T) {
			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
			if err != nil {
				t.Fatalf("CallTool: %v", err)
			}
			text := resultText(res)
			if !res.IsError || !strings.Contains(text, "cannot reach fanind at "+url) {
				t.Errorf("IsError=%v text=%q; want a tool error naming the daemon URL", res.IsError, text)
			}
		})
	}
}

func TestOwnerMCPMissingArguments(t *testing.T) {
	cs := connect(t, client.New("http://127.0.0.1:1", "", nil), "http://127.0.0.1:1")
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: ToolAnswer,
		Arguments: map[string]any{"task": "a1", "answer": ""}})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError || !strings.Contains(resultText(res), "needs task and answer") {
		t.Errorf("IsError=%v text=%q; want the missing-argument error before any daemon call", res.IsError, resultText(res))
	}
}

func resultText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}
