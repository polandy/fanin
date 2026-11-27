package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/polandy/fanin/internal/api/client"
	"github.com/polandy/fanin/internal/api/wire"
	"github.com/polandy/fanin/internal/cli"
	"github.com/polandy/fanin/internal/intake"
	"github.com/polandy/fanin/internal/model"
)

// The task actions.
const (
	taskAdd    = "add"
	taskList   = "list"
	taskImport = "import"
	taskCancel = "cancel"
)

func init() {
	register(Command{
		Name:    "task",
		Summary: "queue, list and cancel tasks: task add|list|import|cancel (--project, --task or --analyze, --reason)",
		Run:     runTask,
	})
}

func runTask(ctx context.Context, g Globals, out cli.Output, args []string) error {
	if len(args) == 0 {
		return cli.Usagef("task needs an action: fanin task %s|%s|%s|%s", taskAdd, taskList, taskImport, taskCancel)
	}
	switch args[0] {
	case taskAdd:
		return runTaskAdd(ctx, g, out, args[1:])
	case taskList:
		return runTaskList(ctx, g, out, args[1:])
	case taskImport:
		return runTaskImport(ctx, g, out, args[1:])
	case taskCancel:
		return runTaskCancel(ctx, g, out, args[1:])
	}
	return cli.Usagef("unknown task action %q; use %s, %s, %s or %s", args[0], taskAdd, taskList, taskImport,
		taskCancel)
}

// runTaskCancel ends a task for good with the owner's token (SCENARIOS OA-8).
func runTaskCancel(ctx context.Context, g Globals, out cli.Output, args []string) error {
	const usage = "task cancel needs exactly one task: fanin task cancel <task> [--reason TEXT]"
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return cli.Usagef(usage)
	}
	id := args[0]
	fs := flag.NewFlagSet("task "+taskCancel, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	reason := fs.String("reason", "", "why the task ends")
	if err := parseTaskFlags(fs, args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return cli.Usagef(usage)
	}
	c, err := daemonClient(g, "task cancel")
	if err != nil {
		return err
	}
	ack, err := c.Cancel(ctx, id, *reason)
	if err != nil {
		return fmt.Errorf("task cancel %s: %w", id, err)
	}
	out.Println(id + ": " + ack.Detail)
	return nil
}

func taskFlags(name string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet("task "+name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs, fs.String("project", "", "project name")
}

func parseTaskFlags(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return cli.Usagef("%v", err)
	}
	return nil
}

type afterFlag []string

func (a *afterFlag) String() string     { return strings.Join(*a, ",") }
func (a *afterFlag) Set(v string) error { *a = append(*a, v); return nil }

func runTaskAdd(ctx context.Context, g Globals, out cli.Output, args []string) error {
	fs, project := taskFlags(taskAdd)
	var after afterFlag
	task := fs.String("task", "", "owner task text")
	analyze := fs.String("analyze", "", "owner question to analyse")
	mdl := fs.String("model", "", "worker model")
	worker := fs.String("worker", "", "worker kind")
	fs.Var(&after, "after", "task that must be done first (repeatable)")
	if err := parseTaskFlags(fs, args); err != nil {
		return err
	}
	req := wire.TaskRequest{Project: *project, Task: *task, Analyze: *analyze, Model: *mdl, Worker: *worker, After: after}
	if *project == "" || fs.NArg() != 0 {
		return cli.Usagef("task add needs --project and --task or --analyze: fanin task add --project P --task TEXT")
	}
	if err := (intake.TaskRequest{Task: *task, Analyze: *analyze, Worker: *worker}).CheckFlags(); err != nil {
		return cli.Usagef("%v", err)
	}
	c, err := daemonClient(g, "task add")
	if err != nil {
		return err
	}
	t, err := c.AddTask(ctx, req)
	if err != nil {
		return taskError("task add", err)
	}
	out.Println(t.ID)
	return nil
}

// taskError maps a refused request to a usage error; the daemon queued nothing.
func taskError(what string, err error) error {
	var ce *client.Error
	if errors.As(err, &ce) && ce.Status == http.StatusBadRequest {
		return cli.Usagef("%s: %s", what, ce.Message)
	}
	return fmt.Errorf("%s: %w", what, err)
}

func runTaskList(ctx context.Context, g Globals, out cli.Output, args []string) error {
	fs, project := taskFlags(taskList)
	if err := parseTaskFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return cli.Usagef("task list takes no arguments besides --project")
	}
	c, err := daemonClient(g, "task list")
	if err != nil {
		return err
	}
	ts, err := c.Tasks(ctx, *project)
	if err != nil {
		return fmt.Errorf("task list: %w", err)
	}
	for _, t := range ts.Tasks {
		line := fmt.Sprintf("%s\t%s\t%s\t%s", t.ID, t.Project, t.State, t.Title)
		if len(t.After) > 0 {
			line += "\tafter " + strings.Join(t.After, ",")
		}
		out.Println(line)
	}
	return nil
}

// importer adapts the API client to intake.Enqueuer; a task that exists maps back to intake.ErrExists.
type importer struct{ c *client.Client }

func (i importer) Enqueue(ctx context.Context, r intake.TaskRequest) (model.Task, error) {
	after := make([]string, 0, len(r.After))
	for _, id := range r.After {
		after = append(after, string(id))
	}
	_, err := i.c.AddTask(ctx, wire.TaskRequest{ID: string(r.ID), Project: r.Project, Task: r.Task, Model: r.Model,
		After: after})
	var ce *client.Error
	if errors.As(err, &ce) && ce.Status == http.StatusConflict {
		return model.Task{}, fmt.Errorf("%w: %s", intake.ErrExists, ce.Message)
	}
	return model.Task{}, err
}

func runTaskImport(ctx context.Context, g Globals, out cli.Output, args []string) error {
	// The file comes first: the flag package stops at the first non-flag.
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return cli.Usagef("task import needs a chain file first: fanin task import <file> --project P")
	}
	file := args[0]
	fs, project := taskFlags(taskImport)
	if err := parseTaskFlags(fs, args[1:]); err != nil {
		return err
	}
	if *project == "" || fs.NArg() != 0 {
		return cli.Usagef("task import needs --project: fanin task import <file> --project P")
	}
	f, err := os.Open(filepath.Clean(file))
	if err != nil {
		return fmt.Errorf("task import: %w; give the path of a chain file", err)
	}
	reqs, perr := intake.ChainRequests(*project, file, f)
	if cerr := f.Close(); perr == nil && cerr != nil {
		perr = fmt.Errorf("task import: close %s: %w", file, cerr)
	}
	if perr != nil {
		if errors.Is(perr, intake.ErrInvalid) {
			return cli.Usagef("%v", perr)
		}
		return perr
	}
	c, err := daemonClient(g, "task import")
	if err != nil {
		return err
	}
	added, skipped, err := intake.Import(ctx, importer{c}, reqs)
	if err != nil {
		return taskError("task import", err)
	}
	out.Printf("%s: %d added, %d skipped\n", file, added, skipped)
	return nil
}
