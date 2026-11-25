package sched

import (
	"bytes"
	"context"
	"sync"

	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/pick"
	"github.com/polandy/fanin/internal/runtime"
)

// fakeRuntime records every start and observe; each observed agent gets a channel the test feeds.
type fakeRuntime struct {
	mu         sync.Mutex
	specs      []runtime.RunSpec
	observed   []runtime.RunHandle
	startErr   error
	observeErr error
	promptErr  error
	stopErr    error
	// prompts are the texts sent per agent; stopped are the stopped agents.
	prompts map[string][]string
	stopped []string
	// block makes Start wait for its ctx.
	block bool
	feeds map[string]chan runtime.RunEvent
}

func newFakeRuntime() *fakeRuntime {
	return &fakeRuntime{feeds: map[string]chan runtime.RunEvent{}, prompts: map[string][]string{}}
}

func (f *fakeRuntime) Start(ctx context.Context, spec runtime.RunSpec) (runtime.RunHandle, error) {
	f.mu.Lock()
	f.specs = append(f.specs, spec)
	block := f.block
	f.mu.Unlock()
	if block {
		<-ctx.Done()
		return runtime.RunHandle{}, ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return runtime.RunHandle{}, f.startErr
	}
	return runtime.RunHandle{TaskID: spec.TaskID, RunID: spec.RunID, Project: spec.Project, Agent: spec.Agent,
		Workspace: "ws-" + spec.Project, Tab: "tab-" + spec.Agent, Pane: "pane-" + spec.Agent}, nil
}

func (f *fakeRuntime) Prompt(_ context.Context, h runtime.RunHandle, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prompts[h.Agent] = append(f.prompts[h.Agent], text)
	return f.promptErr
}

func (f *fakeRuntime) Stop(_ context.Context, h runtime.RunHandle) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, h.Agent)
	return f.stopErr
}

func (f *fakeRuntime) sent(agent string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.prompts[agent]...)
}

func (f *fakeRuntime) stops() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.stopped...)
}

func (f *fakeRuntime) Observe(ctx context.Context, h runtime.RunHandle) (<-chan runtime.RunEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observed = append(f.observed, h)
	if f.observeErr != nil {
		return nil, f.observeErr
	}
	in := f.feed(h.Agent)
	out := make(chan runtime.RunEvent)
	go func() {
		defer close(out)
		for {
			select {
			case ev, ok := <-in:
				if !ok {
					return
				}
				select {
				case out <- ev:
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func (f *fakeRuntime) feed(agent string) chan runtime.RunEvent {
	if f.feeds[agent] == nil {
		f.feeds[agent] = make(chan runtime.RunEvent)
	}
	return f.feeds[agent]
}

func (f *fakeRuntime) starts() []runtime.RunSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]runtime.RunSpec(nil), f.specs...)
}

func (f *fakeRuntime) observes() []runtime.RunHandle {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]runtime.RunHandle(nil), f.observed...)
}

// fakePicker answers every pick with res and err and records the requests.
type fakePicker struct {
	mu   sync.Mutex
	reqs []pick.Request
	res  pick.Result
	err  error
	// block makes Pick wait for its ctx.
	block bool
}

func (f *fakePicker) Pick(ctx context.Context, req pick.Request) (pick.Result, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	res, err, block := f.res, f.err, f.block
	f.mu.Unlock()
	if block {
		<-ctx.Done()
		return pick.Result{}, ctx.Err()
	}
	return res, err
}

func (f *fakePicker) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

// fakeUnpushed answers every scan with commits and err and records the branches asked for.
type fakeUnpushed struct {
	mu       sync.Mutex
	commits  []string
	err      error
	branches []string
}

func (f *fakeUnpushed) Unpushed(_ context.Context, _ config.Project, branch string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.branches = append(f.branches, branch)
	return f.commits, f.err
}

func (f *fakeUnpushed) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.branches...)
}

// syncBuffer is a log sink the loop and its goroutines write concurrently.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
