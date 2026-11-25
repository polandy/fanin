package intake

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/store"
)

const (
	projGH      = "alpha"
	projGitea   = "beta"
	kindClaude  = "claude"
	modelOpus   = "opus"
	modelSonnet = "sonnet"
)

func testConfig() config.Config {
	return config.Config{
		Forges: map[string]config.Forge{
			"gh":    {Kind: config.ForgeGitHub},
			"gitea": {Kind: config.ForgeGitea},
		},
		WorkerKinds: map[string]config.WorkerKind{
			kindClaude:   {Runtime: config.RuntimeHerdr, Models: []string{modelOpus, modelSonnet}, DefaultModel: modelSonnet},
			KindOpencode: {Runtime: config.RuntimeHeadless},
		},
		Projects: []config.Project{
			{Name: projGH, Forge: "gh", WorkerKind: kindClaude},
			{Name: projGitea, Forge: "gitea", WorkerKind: kindClaude},
		},
	}
}

func newService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "fanin.db"),
		clock.NewManual(time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return New(testConfig(), st), st
}

func openTasks(t *testing.T, st *store.Store) []model.Task {
	t.Helper()
	ts, err := st.OpenTasks(context.Background())
	if err != nil {
		t.Fatalf("OpenTasks: %v", err)
	}
	return ts
}

func testPK6OwnerTaskIsMarked(t *testing.T) {
	svc, _ := newService(t)
	got, err := svc.Enqueue(context.Background(), TaskRequest{Project: projGH, Task: "fix the build\nmore", Model: modelOpus})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if !got.OwnerTask || got.Shape != model.ShapePR || got.State != model.TaskQueued || got.OwnerModel != modelOpus ||
		got.Title != "fix the build" || got.Prompt != "fix the build\nmore" {
		t.Errorf("task = %+v; want a queued owner PR task titled by the first line", got)
	}
}

func testPK6AnalyzeMakesAnAnalysisTask(t *testing.T) {
	svc, _ := newService(t)
	got, err := svc.Enqueue(context.Background(), TaskRequest{Project: projGH, Analyze: "why is CI slow?"})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if !got.OwnerTask || got.Shape != model.ShapeAnalysis || got.Prompt != "why is CI slow?" {
		t.Errorf("task = %+v; want an owner task of shape analysis", got)
	}
}

func testPK7UsageErrorsQueueNothing(t *testing.T) {
	tests := []struct {
		name string
		req  TaskRequest
	}{
		{"task and analyze", TaskRequest{Project: projGH, Task: "a", Analyze: "b"}},
		{"neither task nor analyze", TaskRequest{Project: projGH}},
		{"opencode without task", TaskRequest{Project: projGH, Analyze: "b", Worker: KindOpencode}},
		{"unknown model", TaskRequest{Project: projGH, Task: "a", Model: "gpt-0"}},
		{"unknown worker", TaskRequest{Project: projGH, Task: "a", Worker: "nope"}},
		{"model for a kind without models", TaskRequest{Project: projGH, Task: "a", Worker: KindOpencode, Model: modelOpus}},
		{"unknown dependency", TaskRequest{Project: projGH, Task: "a", After: []model.TaskID{"ghost"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, st := newService(t)
			if _, err := svc.Enqueue(context.Background(), tt.req); !errors.Is(err, ErrInvalid) {
				t.Errorf("Enqueue err = %v; want ErrInvalid", err)
			}
			if n := len(openTasks(t, st)); n != 0 {
				t.Errorf("%d tasks queued; want none", n)
			}
		})
	}
}

func testIntakeUnknownProject(t *testing.T) {
	svc, _ := newService(t)
	if _, err := svc.Enqueue(context.Background(), TaskRequest{Project: "nope", Task: "a"}); !errors.Is(err, ErrUnknownProject) {
		t.Errorf("Enqueue err = %v; want ErrUnknownProject", err)
	}
}

func testSP9OpencodeOnlyForGitHubTasks(t *testing.T) {
	tests := []struct {
		name    string
		req     TaskRequest
		wantErr bool
	}{
		{"github task", TaskRequest{Project: projGH, Task: "a", Worker: KindOpencode}, false},
		{"gitea task", TaskRequest{Project: projGitea, Task: "a", Worker: KindOpencode}, true},
		{"github analysis", TaskRequest{Project: projGH, Analyze: "a", Worker: KindOpencode}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _ := newService(t)
			got, err := svc.Enqueue(context.Background(), tt.req)
			if tt.wantErr != errors.Is(err, ErrInvalid) {
				t.Fatalf("Enqueue err = %v; want refusal %v", err, tt.wantErr)
			}
			if !tt.wantErr && got.WorkerKind != KindOpencode {
				t.Errorf("worker kind = %q, want %q", got.WorkerKind, KindOpencode)
			}
		})
	}
}

const chainFile = "# first batch\nopus|do one\n\nsonnet|do two\n"

func testCH1ChainFileBecomesAfterChain(t *testing.T) {
	svc, st := newService(t)
	reqs, err := ChainRequests(projGH, "batch.chain", strings.NewReader(chainFile))
	if err != nil {
		t.Fatalf("ChainRequests: %v", err)
	}
	added, skipped, err := Import(context.Background(), svc, reqs)
	if err != nil || added != 2 || skipped != 0 {
		t.Fatalf("Import = %d, %d, %v; want 2, 0, nil", added, skipped, err)
	}
	ts := openTasks(t, st)
	if len(ts) != 2 {
		t.Fatalf("%d tasks, want 2", len(ts))
	}
	if ts[0].ID != "alpha-batch-2" || ts[1].ID != "alpha-batch-4" {
		t.Errorf("ids = %s, %s; want the line numbers 2 and 4 counted over comment and blank lines", ts[0].ID, ts[1].ID)
	}
	if diff := cmp.Diff([]model.TaskID{"alpha-batch-2"}, ts[1].After); diff != "" || ts[0].After != nil {
		t.Errorf("after chain (-want +got):\n%s", diff)
	}
	if !ts[0].OwnerTask || ts[0].OwnerModel != modelOpus || ts[1].OwnerModel != modelSonnet || ts[0].Prompt != "do one" {
		t.Errorf("tasks = %+v", ts)
	}
}

func testCH1ReimportOfAChangedFileAddsOnlyNew(t *testing.T) {
	svc, st := newService(t)
	ctx := context.Background()
	for i, file := range []string{chainFile, chainFile + "# tail\nopus|do three\n"} {
		reqs, err := ChainRequests(projGH, "batch.chain", strings.NewReader(file))
		if err != nil {
			t.Fatalf("ChainRequests: %v", err)
		}
		added, skipped, err := Import(ctx, svc, reqs)
		wantAdded, wantSkipped := []int{2, 1}[i], []int{0, 2}[i]
		if err != nil || added != wantAdded || skipped != wantSkipped {
			t.Fatalf("import %d = %d, %d, %v; want %d, %d, nil", i, added, skipped, err, wantAdded, wantSkipped)
		}
	}
	ts := openTasks(t, st)
	if len(ts) != 3 || ts[2].ID != "alpha-batch-6" || len(ts[2].After) != 1 || ts[2].After[0] != "alpha-batch-4" {
		t.Errorf("tasks = %+v; want a third task after the second", ts)
	}
}

func testCH1MalformedChainLineNamesLine(t *testing.T) {
	_, err := ChainRequests(projGH, "batch.chain", strings.NewReader("opus|ok\nno separator here\n"))
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "line 2") {
		t.Errorf("err = %v; want ErrInvalid naming line 2", err)
	}
}

func TestIntake(t *testing.T) {
	t.Run("PK-6_owner_task_is_marked", testPK6OwnerTaskIsMarked)
	t.Run("PK-6_analyze_makes_an_analysis_task", testPK6AnalyzeMakesAnAnalysisTask)
	t.Run("PK-7_usage_errors_queue_nothing", testPK7UsageErrorsQueueNothing)
	t.Run("unknown_project", testIntakeUnknownProject)
	t.Run("SP-9_opencode_only_for_github_tasks", testSP9OpencodeOnlyForGitHubTasks)
	t.Run("CH-1_chain_file_becomes_after_chain", testCH1ChainFileBecomesAfterChain)
	t.Run("CH-1_reimport_of_a_changed_file_adds_only_new", testCH1ReimportOfAChangedFileAddsOnlyNew)
	t.Run("CH-1_malformed_chain_line_names_the_line", testCH1MalformedChainLineNamesLine)
}
