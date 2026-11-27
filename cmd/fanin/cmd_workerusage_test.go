package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/cli"
	"github.com/polandy/fanin/pkg/faninmcp"
)

const statusJSON = `{"session_id":"s","context_window":{"total_input_tokens":15500,"total_output_tokens":1200,
	"context_window_size":200000,"used_percentage":8}}`

func runArgsIn(t *testing.T, env map[string]string, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var o, e bytes.Buffer
	code := run(context.Background(), defaultRegistry, args, mapEnv(env),
		cli.Output{In: strings.NewReader(stdin), Out: &o, Err: &e})
	return code, o.String(), e.String()
}

type usageDaemon struct {
	url    string
	paths  []string
	auth   string
	bodies []faninmcp.Usage
}

func newUsageDaemon(t *testing.T, status int) *usageDaemon {
	t.Helper()
	d := &usageDaemon{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var u faninmcp.Usage
		if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
			t.Errorf("decode the body: %v", err)
		}
		d.paths, d.auth, d.bodies = append(d.paths, r.URL.Path), r.Header.Get("Authorization"), append(d.bodies, u)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	t.Cleanup(srv.Close)
	d.url = srv.URL
	return d
}

func (d *usageDaemon) env() map[string]string {
	return map[string]string{faninmcp.EnvURL: d.url, faninmcp.EnvRunID: "r1", faninmcp.EnvRunToken: reportToken}
}

// AW-14: the status line posts the context tokens and share with the run token and prints a short line.
func TestWorkerUsagePostsTheStatus(t *testing.T) {
	d := newUsageDaemon(t, http.StatusOK)
	code, out, stderr := runArgsIn(t, d.env(), statusJSON, "worker-usage")
	if code != cli.ExitOK {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if d.auth != "Bearer "+reportToken || len(d.paths) != 1 || d.paths[0] != faninmcp.UsagePath("r1") {
		t.Errorf("requests = %v with %q, want one to %s", d.paths, d.auth, faninmcp.UsagePath("r1"))
	}
	if diff := cmp.Diff([]faninmcp.Usage{{InputTokens: 15500, OutputTokens: 1200, ContextPct: 8}}, d.bodies); diff != "" {
		t.Errorf("bodies (-want +got):\n%s", diff)
	}
	if out != "fanin r1 · context 8%\n" {
		t.Errorf("stdout = %q", out)
	}
}

// AW-14: before the first response there is nothing to count, and nothing is sent.
func TestWorkerUsageEmptyStatusSendsNothing(t *testing.T) {
	d := newUsageDaemon(t, http.StatusOK)
	code, out, stderr := runArgsIn(t, d.env(), `{"session_id":"s"}`, "worker-usage")
	if code != cli.ExitOK || out != "fanin r1 · context 0%\n" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, stderr)
	}
	if len(d.paths) != 0 {
		t.Errorf("requests = %v, want none", d.paths)
	}
}

// AW-14: a refused post, a broken status or a missing run environment exits non-zero and prints no line.
func TestWorkerUsageFailures(t *testing.T) {
	refused := newUsageDaemon(t, http.StatusUnauthorized)
	cases := []struct {
		name  string
		env   map[string]string
		stdin string
		code  int
	}{
		{"daemon_refuses", refused.env(), statusJSON, cli.ExitError},
		{"status_is_no_json", refused.env(), "nope", cli.ExitError},
		{"no_run_environment", map[string]string{}, statusJSON, cli.ExitUsage},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out, stderr := runArgsIn(t, tc.env, tc.stdin, "worker-usage")
			if code != tc.code || out != "" {
				t.Errorf("exit %d, stdout %q; want exit %d and no line", code, out, tc.code)
			}
			if strings.Contains(stderr, reportToken) {
				t.Errorf("stderr carries the token: %q", stderr)
			}
		})
	}
}
