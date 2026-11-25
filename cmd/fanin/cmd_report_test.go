package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/cli"
	"github.com/polandy/fanin/pkg/faninmcp"
)

const (
	reportToken  = "run-secret"
	reportBranch = "feat/report"
	flagText     = "--text"
)

var (
	kindDonePR  = string(faninmcp.KindDonePR)
	kindIssue   = string(faninmcp.KindIssue)
	kindBlocked = string(faninmcp.KindBlocked)
)

func fileEnv() map[string]string {
	return map[string]string{faninmcp.EnvRunToken: reportToken}
}

func validateAgainstSchema(t *testing.T, data []byte) {
	t.Helper()
	s, err := faninmcp.ResultFileSchema()
	if err != nil {
		t.Fatal(err)
	}
	rs, err := s.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("result file is not JSON: %v", err)
	}
	if err := rs.Validate(doc); err != nil {
		t.Errorf("result file does not match the schema: %v\n%s", err, data)
	}
}

// C-REPORT: the written file validates against the schema and parses back to the report.
func TestCReportWritesSchemaValidFile(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want faninmcp.Report
	}{
		{kindDonePR, []string{kindDonePR, "--pr", "42"}, faninmcp.Report{Kind: faninmcp.KindDonePR, PR: 42}},
		{"pushed", []string{"pushed", "--branch", reportBranch}, faninmcp.Report{Kind: faninmcp.KindPushed, Branch: reportBranch}},
		{"committed", []string{"committed", "--branch", reportBranch}, faninmcp.Report{Kind: faninmcp.KindCommitted, Branch: reportBranch}},
		{kindIssue, []string{kindIssue, "--issue", "7", "--issue-url", "https://example.com/i/7"},
			faninmcp.Report{Kind: faninmcp.KindIssue, Issue: 7, IssueURL: "https://example.com/i/7"}},
		{"nothing", []string{"nothing", flagText, "already fixed"}, faninmcp.Report{Kind: faninmcp.KindNothing, Text: "already fixed"}},
		{kindBlocked, []string{kindBlocked, flagText, "which db?"}, faninmcp.Report{Kind: faninmcp.KindBlocked, Text: "which db?"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			args := append([]string{"report", "--dir", dir}, tt.args...)
			code, _, stderr := runArgs(t, defaultRegistry, fileEnv(), args...)
			if code != cli.ExitOK {
				t.Fatalf("exit %d, stderr %q", code, stderr)
			}
			data, err := os.ReadFile(filepath.Join(dir, faninmcp.ResultFileName))
			if err != nil {
				t.Fatal(err)
			}
			validateAgainstSchema(t, data)
			got, err := faninmcp.ParseResultFile(data)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(faninmcp.ResultFile{Token: reportToken, Report: tt.want}, got); diff != "" {
				t.Errorf("result file (-want +got):\n%s", diff)
			}
		})
	}
}

// C-REPORT: an invalid kind or a missing field is exit 2 and writes nothing.
func TestCReportInvalidWritesNothing(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		args []string
	}{
		{"unknown_kind", fileEnv(), []string{"finished"}},
		{"no_kind", fileEnv(), nil},
		{"done_pr_without_pr", fileEnv(), []string{"done_pr"}},
		{"blocked_without_text", fileEnv(), []string{"blocked"}},
		{"bad_issue_url", fileEnv(), []string{"issue", "--issue", "1", "--issue-url", "not a url"}},
		{"no_token", map[string]string{}, []string{"nothing", flagText, "x"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			args := append([]string{"report", "--dir", dir}, tt.args...)
			code, _, stderr := runArgs(t, defaultRegistry, tt.env, args...)
			if code != cli.ExitUsage {
				t.Errorf("exit %d, want %d; stderr %q", code, cli.ExitUsage, stderr)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Errorf("run directory has %d entries, want none", len(entries))
			}
		})
	}
}

// C-REPORT: a failed write leaves an old result.json unchanged and exits 1.
func TestCReportWriteIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, faninmcp.ResultFileName)
	const old = `{"token":"old","kind":"nothing","text":"old"}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if f, err := os.CreateTemp(dir, "probe"); err == nil {
		_ = f.Close()
		t.Skip("the directory is writable despite mode 0500 (running as root)")
	}
	code, _, stderr := runArgs(t, defaultRegistry, fileEnv(), "report", "--dir", dir, "nothing", flagText, "new")
	if code != cli.ExitError {
		t.Errorf("exit %d, want %d; stderr %q", code, cli.ExitError, stderr)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != old {
		t.Errorf("result.json = %q, want it unchanged", got)
	}
}

// C-REPORT: without --dir the report goes to the daemon with the run token as bearer.
func TestCReportSendsToDaemon(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody faninmcp.Report
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	t.Cleanup(srv.Close)
	env := map[string]string{faninmcp.EnvURL: srv.URL, faninmcp.EnvRunID: "r1", faninmcp.EnvRunToken: reportToken}
	code, _, stderr := runArgs(t, defaultRegistry, env, "report", "done_pr", "--pr", "42")
	if code != cli.ExitOK {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if gotPath != faninmcp.ReportPath("r1") || gotAuth != "Bearer "+reportToken {
		t.Errorf("request = %s with %q", gotPath, gotAuth)
	}
	if diff := cmp.Diff(faninmcp.Report{Kind: faninmcp.KindDonePR, PR: 42}, gotBody); diff != "" {
		t.Errorf("body (-want +got):\n%s", diff)
	}
}

// C-REPORT: a daemon that refuses the token is exit 1 and the token stays out of the output.
func TestCReportWrongTokenExitsOne(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"not the run token"}}`))
	}))
	t.Cleanup(srv.Close)
	env := map[string]string{faninmcp.EnvURL: srv.URL, faninmcp.EnvRunID: "r1", faninmcp.EnvRunToken: reportToken}
	code, _, stderr := runArgs(t, defaultRegistry, env, "report", "nothing", flagText, "x")
	if code != cli.ExitError {
		t.Errorf("exit %d, want %d", code, cli.ExitError)
	}
	if !strings.Contains(stderr, "401") || strings.Contains(stderr, reportToken) {
		t.Errorf("stderr %q: want the 401 and no token", stderr)
	}
}

// C-REPORT: no --dir and no daemon environment is a usage error naming both ways.
func TestCReportNeedsATarget(t *testing.T) {
	code, _, stderr := runArgs(t, defaultRegistry, fileEnv(), "report", "nothing", flagText, "x")
	if code != cli.ExitUsage || !strings.Contains(stderr, "--dir") || !strings.Contains(stderr, faninmcp.EnvURL) {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
}
