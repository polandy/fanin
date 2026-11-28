// Package intake turns every task source into one TaskRequest and queues it in the store (concept: Intake;
// spec in docs/dev/spec/intake.md).
package intake

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/scope"
	"github.com/polandy/fanin/internal/store"
)

// KindOpencode is the name of the worker kind that runs in the opencode sandbox (see SCENARIOS SP-9).
const KindOpencode = "opencode"

// maxTitleRunes caps the title taken from the first line of a task text.
const maxTitleRunes = 80

// The intake errors; the API maps them to status codes.
var (
	// ErrInvalid is a request that breaks a rule; nothing was queued.
	ErrInvalid = errors.New("intake: invalid task request")
	// ErrUnknownProject is a project the config does not name.
	ErrUnknownProject = errors.New("intake: unknown project")
	// ErrExists is a request whose task ID is already queued.
	ErrExists = errors.New("intake: task exists")
)

// TaskRequest is what every source (CLI, chain file, webhook) asks for.
type TaskRequest struct {
	// ID names the task; empty takes the next "<project>-<n>".
	ID      model.TaskID
	Project string
	// Task is an owner task (shape pr); Analyze is an owner question (shape analysis). Exactly one is set.
	Task    string
	Analyze string
	// Model and Worker override the project's model and worker kind.
	Model  string
	Worker string
	// After lists the tasks that must be done first.
	After []model.TaskID
	// AllowedPaths is the task's file scope; see scope.Validate for its rules (SCENARIOS SC-1).
	AllowedPaths []string
}

// Service queues requests in the store, checked against the config.
type Service struct {
	cfg config.Config
	st  *store.Store
}

// New returns a Service over cfg and st.
func New(cfg config.Config, st *store.Store) *Service { return &Service{cfg: cfg, st: st} }

func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// CheckFlags checks the rules that need no config: exactly one of Task and Analyze, opencode only for a task and
// a valid scope (see SCENARIOS PK-7, SC-1).
func (r TaskRequest) CheckFlags() error {
	switch {
	case strings.TrimSpace(r.Task) != "" && strings.TrimSpace(r.Analyze) != "":
		return invalidf("task and analyze together; give the owner task or the owner question, not both")
	case strings.TrimSpace(r.Task) == "" && strings.TrimSpace(r.Analyze) == "":
		return invalidf("no task text; give the task or the analysis question")
	case r.Worker == KindOpencode && strings.TrimSpace(r.Task) == "":
		return invalidf("worker %s without a task; opencode takes owner tasks only, never analyses", KindOpencode)
	}
	if err := scope.Validate(r.AllowedPaths); err != nil {
		return fmt.Errorf("%w: allowed_paths: %w", ErrInvalid, err)
	}
	return nil
}

// Validate checks r against the config and returns the project it names.
func (r TaskRequest) Validate(cfg config.Config) (config.Project, error) {
	p, ok := cfg.Project(r.Project)
	if !ok {
		return config.Project{}, fmt.Errorf("%w: %q; configured: %s", ErrUnknownProject, r.Project,
			strings.Join(cfg.ProjectNames(), ", "))
	}
	if err := r.CheckFlags(); err != nil {
		return p, err
	}
	kindName := cmp.Or(r.Worker, p.WorkerKind)
	kind, ok := cfg.WorkerKinds[kindName]
	if !ok {
		return p, invalidf("worker kind %q is not configured; use one of the kinds in worker-kinds.yaml", kindName)
	}
	if kindName == KindOpencode {
		if cfg.Forges[p.Forge].Kind != config.ForgeGitHub {
			return p, invalidf("worker %s on project %s: opencode runs for GitHub projects only", KindOpencode, p.Name)
		}
		if strings.TrimSpace(r.Task) == "" {
			return p, invalidf("worker %s on an analysis; opencode never runs analyses", KindOpencode)
		}
	}
	if r.Model != "" && !slices.Contains(kind.Models, r.Model) {
		return p, invalidf("model %q is not a model of worker kind %s (%s); pick one of its models", r.Model, kindName,
			strings.Join(kind.Models, ", "))
	}
	return p, nil
}

// Enqueue validates r and queues it as a task in one transaction; nothing is queued on an error.
func (s *Service) Enqueue(ctx context.Context, r TaskRequest) (model.Task, error) {
	if _, err := r.Validate(s.cfg); err != nil {
		return model.Task{}, err
	}
	var task model.Task
	err := s.st.Transition(ctx, func(ctx context.Context, tx *store.Tx) error {
		for _, dep := range r.After {
			d, err := tx.Task(ctx, dep)
			switch {
			case errors.Is(err, store.ErrNotFound):
				return invalidf("after %q is no task; list the tasks with 'fanin task list'", dep)
			case err != nil:
				return err
			case d.Project != r.Project:
				return invalidf("after %q belongs to project %s, not %s", dep, d.Project, r.Project)
			}
		}
		task = r.task()
		if task.ID == "" {
			id, err := tx.NextTaskID(ctx, r.Project)
			if err != nil {
				return err
			}
			task.ID = id
		} else if _, err := tx.Task(ctx, task.ID); err == nil {
			return fmt.Errorf("%w: %q", ErrExists, task.ID)
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		return tx.CreateTask(ctx, task)
	})
	if err != nil {
		return model.Task{}, err
	}
	return s.st.Task(ctx, task.ID)
}

func (r TaskRequest) task() model.Task {
	text, shape := r.Task, model.ShapePR
	if strings.TrimSpace(r.Analyze) != "" {
		text, shape = r.Analyze, model.ShapeAnalysis
	}
	return model.Task{
		ID: r.ID, Project: r.Project, Title: title(text), Prompt: text, Shape: shape, OwnerTask: true,
		After: r.After, OwnerModel: r.Model, WorkerKind: r.Worker, AllowedPaths: r.AllowedPaths,
	}
}

func title(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	if r := []rune(line); len(r) > maxTitleRunes {
		return string(r[:maxTitleRunes])
	}
	return line
}
