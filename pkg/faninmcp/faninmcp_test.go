package faninmcp

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var update = flag.Bool("update", false, "rewrite the golden files")

const (
	testRun   = "r2"
	testToken = "run-secret"
)

// request is one call the fake daemon received.
type request struct {
	Path, Auth, Body string
}

// fakeDaemon records every request and answers with status and body.
type fakeDaemon struct {
	mu     sync.Mutex
	got    []request
	status int
	body   string
}

func (f *fakeDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	f.mu.Lock()
	f.got = append(f.got, request{Path: r.URL.Path, Auth: r.Header.Get("Authorization"), Body: string(b)})
	f.mu.Unlock()
	w.WriteHeader(f.status)
	_, _ = io.WriteString(w, f.body)
}

func newClient(t *testing.T, url string) *Client {
	t.Helper()
	c, err := NewClient(url, testRun, testToken, nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

// connect serves NewServer(c) in memory and returns a connected client session.
func connect(t *testing.T, c *Client) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	srv, err := NewServer(c, "test")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
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

func call(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
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

func golden(t *testing.T, name string, v any) {
	t.Helper()
	got, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden: %v; run go test ./pkg/faninmcp -update", err)
	}
	if string(got) != string(want) {
		t.Errorf("%s differs; run go test ./pkg/faninmcp -update and review the diff\ngot:\n%s", path, got)
	}
}

func TestToolSchemasGolden(t *testing.T) {
	cs := connect(t, newClient(t, "http://127.0.0.1:1"))
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	golden(t, "tools.golden.json", res.Tools)
}

func TestResultFileSchemaGolden(t *testing.T) {
	s, err := ResultFileSchema()
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "result.schema.json", s)
}

func TestToolsForwardToTheDaemon(t *testing.T) {
	fake := &fakeDaemon{status: http.StatusOK, body: `{"status":"ok"}`}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	cs := connect(t, newClient(t, srv.URL))
	text, isErr := call(t, cs, ToolReport, map[string]any{kindProperty: "done_pr", "pr": 42})
	if isErr || text != "reported done_pr" {
		t.Errorf("%s = %q (error %v), want reported done_pr", ToolReport, text, isErr)
	}
	text, isErr = call(t, cs, ToolProgress, map[string]any{"text": "tests running"})
	if isErr || text != "progress noted" {
		t.Errorf("%s = %q (error %v), want progress noted", ToolProgress, text, isErr)
	}
	want := []request{
		{Path: "/api/v1/runs/r2/report", Auth: "Bearer " + testToken, Body: `{"kind":"done_pr","pr":42}`},
		{Path: "/api/v1/runs/r2/progress", Auth: "Bearer " + testToken, Body: `{"text":"tests running"}`},
	}
	if diff := cmp.Diff(want, fake.got); diff != "" {
		t.Errorf("requests (-want +got):\n%s", diff)
	}
}

func TestToolErrors(t *testing.T) {
	t.Run("daemon_refusal_is_a_tool_error", func(t *testing.T) {
		fake := &fakeDaemon{status: http.StatusConflict,
			body: `{"error":{"code":"conflict","message":"not the current run"}}`}
		srv := httptest.NewServer(fake)
		t.Cleanup(srv.Close)
		text, isErr := call(t, connect(t, newClient(t, srv.URL)), ToolReport,
			map[string]any{kindProperty: "nothing", "text": "x"})
		if !isErr || !strings.Contains(text, "409 conflict: not the current run") {
			t.Errorf("text = %q (error %v), want the daemon's refusal", text, isErr)
		}
	})
	t.Run("unknown_kind_is_refused_before_any_call", func(t *testing.T) {
		fake := &fakeDaemon{status: http.StatusOK}
		srv := httptest.NewServer(fake)
		t.Cleanup(srv.Close)
		_, isErr := call(t, connect(t, newClient(t, srv.URL)), ToolReport, map[string]any{kindProperty: "finished"})
		if !isErr || len(fake.got) != 0 {
			t.Errorf("error %v, requests %v; want a tool error and no request", isErr, fake.got)
		}
	})
	t.Run("connection_failure_names_the_daemon_and_the_fallback", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		url := srv.URL
		srv.Close()
		text, isErr := call(t, connect(t, newClient(t, url)), ToolProgress, map[string]any{})
		if !isErr || !strings.Contains(text, url) || !strings.Contains(text, ResultFileName) {
			t.Errorf("text = %q (error %v), want the URL and %s", text, isErr, ResultFileName)
		}
	})
}

func TestClientErrorWithoutJSON(t *testing.T) {
	srv := httptest.NewServer(&fakeDaemon{status: http.StatusBadGateway, body: "proxy down\n"})
	t.Cleanup(srv.Close)
	err := newClient(t, srv.URL).Progress(context.Background(), Progress{})
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadGateway || apiErr.Message != "proxy down" {
		t.Errorf("error = %v, want 502 with the raw body", err)
	}
}

func TestNewClientNeedsEveryValue(t *testing.T) {
	cases := []struct{ url, run, token, missing string }{
		{"", testRun, testToken, EnvURL},
		{"http://x", "", testToken, EnvRunID},
		{"http://x", testRun, "", EnvRunToken},
	}
	for _, tc := range cases {
		t.Run(tc.missing, func(t *testing.T) {
			_, err := NewClient(tc.url, tc.run, tc.token, nil)
			if !errors.Is(err, ErrConfig) || !strings.Contains(err.Error(), tc.missing) {
				t.Errorf("NewClient = %v, want ErrConfig naming %s", err, tc.missing)
			}
		})
	}
}

func TestParseResultFile(t *testing.T) {
	good := ResultFile{Token: testToken, Report: Report{Kind: KindIssue, Issue: 7,
		IssueURL: "https://forge.example/o/r/issues/7"}}
	got, err := ParseResultFile([]byte(`{"token":"run-secret","kind":"issue","issue":7,` +
		`"issue_url":"https://forge.example/o/r/issues/7"}`))
	if err != nil {
		t.Fatalf("ParseResultFile: %v", err)
	}
	if diff := cmp.Diff(good, got); diff != "" {
		t.Errorf("ParseResultFile (-want +got):\n%s", diff)
	}
	bad := []struct{ name, data string }{
		{"empty", ``},
		{"half_written", `{"token":"run-secret","kind":"iss`},
		{"not_an_object", `["issue"]`},
		{"unknown_field", `{"token":"t","kind":"nothing","text":"x","why":"y"}`},
		{"two_objects", `{"token":"t","kind":"nothing","text":"x"} {}`},
		{"no_token", `{"kind":"nothing","text":"x"}`},
		{"no_kind", `{"token":"t","text":"x"}`},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseResultFile([]byte(tc.data)); !errors.Is(err, ErrUnreadable) {
				t.Errorf("ParseResultFile(%q) = %v, want ErrUnreadable", tc.data, err)
			}
		})
	}
}
