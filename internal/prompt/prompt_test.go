package prompt

import (
	"errors"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/pkg/faninmcp"
)

var update = flag.Bool("update", false, "rewrite the golden files")

const fixtureTemplate = "templates/x.md"

var vars = Vars{Repo: "/r/repo", Base: "main", Hint: "fix it"}

func mustRender(t *testing.T, n Name, v Vars) string {
	t.Helper()
	out, err := Render(n, v)
	if err != nil {
		t.Fatalf("Render(%s): %v", n, err)
	}
	return out
}

func lines(s string) []string { return strings.Split(strings.TrimRight(s, "\n"), "\n") }

func TestPrompt(t *testing.T) {
	t.Run("SP-7_placeholders_filled_and_no_include_left", func(t *testing.T) {
		for _, n := range Names {
			out := mustRender(t, n, vars)
			if strings.Contains(out, "{{") {
				t.Errorf("%s: placeholder left in %q", n, out)
			}
			for _, want := range []string{vars.Repo, vars.Hint} {
				if !strings.Contains(out, want) {
					t.Errorf("%s: %q missing", n, want)
				}
			}
		}
	})

	t.Run("SP-7_hint_is_literal", func(t *testing.T) {
		hint := `a & b | \1 {{REPO}} {{INCLUDE:owner-input}} $x`
		out := mustRender(t, Worker, Vars{Repo: "/r/repo", Base: "main", Hint: hint})
		if !strings.Contains(out, "(may be empty): "+hint+"\n") {
			t.Errorf("hint changed in %q", out)
		}
		if got := strings.Count(out, "Owner input (owner rule)"); got != 1 {
			t.Errorf("owner-input part %d times, want 1 (hint was expanded)", got)
		}
	})

	t.Run("SP-7_missing_part_fails", func(t *testing.T) {
		fsys := fstest.MapFS{fixtureTemplate: {Data: []byte("x\n{{INCLUDE:nope}}\n")}}
		out, err := render(fsys, "x", vars)
		if !errors.Is(err, ErrTemplate) || !errors.Is(err, fs.ErrNotExist) || out != "" {
			t.Errorf("render = %q, %v; want ErrTemplate wrapping ErrNotExist and no output", out, err)
		}
		if !strings.Contains(err.Error(), "nope") {
			t.Errorf("error %q does not name the part", err)
		}
	})

	t.Run("SP-7_missing_template_fails", func(t *testing.T) {
		out, err := Render("absent", vars)
		if !errors.Is(err, ErrTemplate) || out != "" {
			t.Errorf("Render = %q, %v; want ErrTemplate", out, err)
		}
	})

	t.Run("SP-7_unknown_placeholder_fails", func(t *testing.T) {
		fsys := fstest.MapFS{fixtureTemplate: {Data: []byte("x {{HOST}}\n")}}
		if _, err := render(fsys, "x", vars); !errors.Is(err, ErrPlaceholder) {
			t.Errorf("render err = %v, want ErrPlaceholder", err)
		}
	})

	t.Run("SP-7_include_cycle_fails", func(t *testing.T) {
		fsys := fstest.MapFS{
			fixtureTemplate:       {Data: []byte("{{INCLUDE:a}}\n")},
			"templates/parts/a.md": {Data: []byte("{{INCLUDE:a}}\n")},
		}
		if _, err := render(fsys, "x", vars); !errors.Is(err, ErrTemplate) {
			t.Errorf("render err = %v, want ErrTemplate", err)
		}
	})

	t.Run("SP-7_report_instruction_once_before_format_line", func(t *testing.T) {
		for _, n := range Names {
			l := lines(mustRender(t, n, vars))
			if len(l) < 3 {
				t.Fatalf("%s: only %d lines", n, len(l))
			}
			instr, format := l[len(l)-2], l[len(l)-1]
			if !strings.Contains(instr, "`"+faninmcp.ToolReport+"`") || !strings.HasSuffix(instr, "nothing else:") {
				t.Errorf("%s: line before the format line = %q, want the %s instruction", n, instr, faninmcp.ToolReport)
			}
			if !strings.HasPrefix(format, "`") {
				t.Errorf("%s: last line %q is not the format line", n, format)
			}
			if got := strings.Count(strings.Join(l, "\n"), faninmcp.ToolReport); got != 1 {
				t.Errorf("%s: %s appears %d times, want 1", n, faninmcp.ToolReport, got)
			}
		}
	})

	t.Run("SP-7_diff_review_before_first_push_pr_review_after", func(t *testing.T) {
		for _, n := range []Name{Worker} {
			out := mustRender(t, n, vars)
			local := "/pr-review local diff, no PR yet: review git diff origin/main...HEAD"
			i, j := strings.Index(out, local), strings.Index(out, "Then push and open the PR.")
			if i < 0 || j < i {
				t.Errorf("%s: local review at %d, push at %d; want the review first", n, i, j)
			}
			k := strings.Index(out, "Then run /pr-review on your PR right away")
			if k < j {
				t.Errorf("%s: PR review at %d, want it after the push at %d", n, k, j)
			}
		}
	})

	t.Run("SP-7_parts_included_once", func(t *testing.T) {
		herdr := "run `herdr`"
		for n, want := range map[Name]int{Worker: 1, WorkerAnalyze: 1, WorkerOC: 0, WorkerGitea: 1, WorkerGiteaAnalyze: 1} {
			if got := strings.Count(mustRender(t, n, vars), herdr); got != want {
				t.Errorf("%s: %q appears %d times, want %d", n, herdr, got, want)
			}
		}
	})

	t.Run("SP-7_golden", func(t *testing.T) {
		for _, n := range Names {
			t.Run(string(n), func(t *testing.T) {
				got := mustRender(t, n, vars)
				path := filepath.Join("testdata", string(n)+".golden.md")
				if *update {
					if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("read golden: %v (run go test ./internal/prompt -update)", err)
				}
				if d := cmp.Diff(string(want), got); d != "" {
					t.Errorf("%s differs from golden (-want +got):\n%s", n, d)
				}
			})
		}
	})
}
