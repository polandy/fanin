package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/polandy/fanin/internal/atomicfile"
	"github.com/polandy/fanin/internal/cli"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/pkg/faninmcp"
)

// resultFileMode is the permission of result.json: it carries the run token.
const resultFileMode = 0o600

func init() {
	register(Command{
		Name: "report",
		Summary: "report a run's result without MCP: report <kind> [--pr n] [--branch b] [--issue n] " +
			"[--issue-url u] [--text t] [--dir run-dir]",
		Run: runReport,
	})
}

func runReport(ctx context.Context, g Globals, _ cli.Output, args []string) error {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var rep faninmcp.Report
	fs.IntVar(&rep.PR, "pr", 0, "pull request number of done_pr")
	fs.StringVar(&rep.Branch, "branch", "", "branch of pushed and committed")
	fs.IntVar(&rep.Issue, "issue", 0, "issue number of issue")
	fs.StringVar(&rep.IssueURL, "issue-url", "", "web address of the issue")
	fs.StringVar(&rep.Text, "text", "", "reason of nothing, question of blocked, or a note")
	dir := fs.String("dir", "", "run directory to write "+faninmcp.ResultFileName+" into instead of calling the daemon")
	kind, err := parseKindAndFlags(fs, args)
	if err != nil {
		return err
	}
	rep.Kind = faninmcp.Kind(kind)
	if err := validateReport(rep); err != nil {
		return cli.Usagef("report: %v", err)
	}
	token := g.Getenv(faninmcp.EnvRunToken)
	switch {
	case *dir != "":
		return writeResultFile(*dir, token, rep)
	case g.DaemonURL != "" && token != "":
		return sendReport(ctx, g, token, rep)
	default:
		return cli.Usagef("report needs a target: --dir <run directory>, or $%s and $%s for the daemon",
			faninmcp.EnvURL, faninmcp.EnvRunToken)
	}
}

// parseKindAndFlags reads the one positional kind with flags before or after it.
func parseKindAndFlags(fs *flag.FlagSet, args []string) (string, error) {
	if err := fs.Parse(args); err != nil {
		return "", cli.Usagef("report: %v", err)
	}
	if fs.NArg() == 0 {
		return "", cli.Usagef("report needs a kind (%v): fanin report <kind> [flags]", faninmcp.Kinds)
	}
	kind := fs.Arg(0)
	if err := fs.Parse(fs.Args()[1:]); err != nil {
		return "", cli.Usagef("report: %v", err)
	}
	if fs.NArg() != 0 {
		return "", cli.Usagef("report takes one kind and flags only; got extra %q", fs.Args())
	}
	return kind, nil
}

// validateReport applies the daemon's own rules, so a bad report fails here and not as a silent file.
func validateReport(rep faninmcp.Report) error {
	kind, err := model.ParseResultKind(string(rep.Kind))
	if err != nil {
		return err
	}
	return model.Result{Kind: kind, PR: rep.PR, Branch: rep.Branch, Issue: rep.Issue, IssueURL: rep.IssueURL,
		Text: rep.Text}.Validate()
}

func writeResultFile(dir, token string, rep faninmcp.Report) error {
	if token == "" {
		return cli.Usagef("report: $%s is empty; result.json needs the run token", faninmcp.EnvRunToken)
	}
	data, err := json.Marshal(faninmcp.ResultFile{Token: token, Report: rep})
	if err != nil {
		return fmt.Errorf("report: encode %s: %w", faninmcp.ResultFileName, err)
	}
	path := filepath.Join(dir, faninmcp.ResultFileName)
	if err := atomicfile.Write(path, append(data, '\n'), resultFileMode); err != nil {
		return fmt.Errorf("report: write %s: %w", path, err)
	}
	return nil
}

func sendReport(ctx context.Context, g Globals, token string, rep faninmcp.Report) error {
	c, err := faninmcp.NewClient(g.DaemonURL, g.Getenv(faninmcp.EnvRunID), token, nil)
	if err != nil {
		return cli.Usagef("report: %v", err)
	}
	if err := c.Report(ctx, rep); err != nil {
		return fmt.Errorf("report %s: %w", rep.Kind, err)
	}
	return nil
}
