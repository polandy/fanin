package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/polandy/fanin/internal/cli"
)

const spyName = "spy"

func mapEnv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func runArgs(t *testing.T, reg *Registry, env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	var o, e bytes.Buffer
	code := run(context.Background(), reg, args, mapEnv(env), cli.Output{Out: &o, Err: &e})
	return code, o.String(), e.String()
}

// spyRegistry has one command that records whether it ran.
func spyRegistry(t *testing.T, ran *bool, got *Globals) *Registry {
	t.Helper()
	reg := NewRegistry()
	if err := reg.Register(Command{Name: spyName, Summary: "records the call", Run: func(_ context.Context, g Globals, _ cli.Output, _ []string) error {
		*ran = true
		*got = g
		return nil
	}}); err != nil {
		t.Fatal(err)
	}
	return reg
}

// PK-7: usage error with exit 2 before anything happens.
func TestPK7UsageErrorBeforeAnyAction(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"unknown subcommand", []string{"bogus"}, `unknown subcommand "bogus"`},
		{"no subcommand", nil, "no subcommand given"},
		{"unknown global flag", []string{"--nope", spyName}, "flag provided but not defined"},
		{"global flag missing value", []string{"--token"}, "needs an argument"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ran bool
			var g Globals
			code, stdout, stderr := runArgs(t, spyRegistry(t, &ran, &g), nil, tt.args...)
			if code != cli.ExitUsage {
				t.Fatalf("exit = %d, want %d; stderr: %s", code, cli.ExitUsage, stderr)
			}
			if ran {
				t.Fatal("subcommand ran despite usage error")
			}
			if stdout != "" {
				t.Fatalf("stdout = %q, want empty", stdout)
			}
			if !strings.Contains(stderr, tt.want) || !strings.Contains(stderr, "usage: fanin") || !strings.Contains(stderr, spyName) {
				t.Fatalf("stderr = %q, want message %q and usage listing", stderr, tt.want)
			}
		})
	}
}

func TestGlobalFlagsEnvFallback(t *testing.T) {
	env := map[string]string{"FANIN_CONFIG": "/e/c.yaml", "FANIN_URL": "http://e", "FANIN_TOKEN": "et"}
	tests := []struct {
		name string
		env  map[string]string
		args []string
		want Globals
	}{
		{"none", nil, []string{spyName}, Globals{}},
		{"env", env, []string{spyName}, Globals{Config: "/e/c.yaml", DaemonURL: "http://e", Token: "et"}},
		{"flags win", env, []string{"--config", "f.yaml", "--daemon-url=http://f", "--token", "ft", spyName},
			Globals{Config: "f.yaml", DaemonURL: "http://f", Token: "ft"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ran bool
			var got Globals
			code, _, stderr := runArgs(t, spyRegistry(t, &ran, &got), tt.env, tt.args...)
			if code != cli.ExitOK || !ran {
				t.Fatalf("code=%d ran=%v stderr=%s", code, ran, stderr)
			}
			if got.Getenv == nil {
				t.Fatal("Globals.Getenv is nil; run must pass its getenv on")
			}
			got.Getenv = nil
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Fatalf("globals mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRunErrorExitsOne(t *testing.T) {
	reg := NewRegistry()
	_ = reg.Register(Command{Name: "boom", Run: func(context.Context, Globals, cli.Output, []string) error {
		return os.ErrNotExist
	}})
	code, _, stderr := runArgs(t, reg, nil, "boom")
	if code != cli.ExitError || strings.Contains(stderr, "usage:") {
		t.Fatalf("code=%d stderr=%q, want exit 1 without usage text", code, stderr)
	}
}

func TestRegisterRejectsBadCommands(t *testing.T) {
	run := func(context.Context, Globals, cli.Output, []string) error { return nil }
	reg := NewRegistry()
	if err := reg.Register(Command{Name: "a", Run: run}); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]Command{"duplicate": {Name: "a", Run: run}, "no name": {Run: run}, "no run": {Name: "b"}} {
		if err := reg.Register(c); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestVersionGolden(t *testing.T) {
	code, stdout, stderr := runArgs(t, defaultRegistry, nil, "version")
	if code != cli.ExitOK || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "version.golden"))
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(string(want), stdout); diff != "" {
		t.Fatalf("version output mismatch (-want +got):\n%s", diff)
	}
	if code, _, _ := runArgs(t, defaultRegistry, nil, "version", "extra"); code != cli.ExitUsage {
		t.Fatalf("version with argument: exit %d, want %d", code, cli.ExitUsage)
	}
}
