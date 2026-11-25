// Package shadow mirrors the bash orchestrator's state directory into the store (docs/dev/spec/shadow.md).
package shadow

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/polandy/fanin/internal/model"
)

// ErrUnreadable marks a state directory or file that cannot be read or parsed (see SCENARIOS SH-4).
var ErrUnreadable = errors.New("shadow: unreadable bash state")

// The bash orchestrator's line formats (lib.sh, status.sh).
const (
	// IDPrefix starts the ID of every task the importer creates.
	IDPrefix = "shadow-"
	// DefaultLane is the lane of a worker name without a lane letter.
	DefaultLane = "a"
	// OwnerTaskPrefix starts the prompt of a task the owner named.
	OwnerTaskPrefix = "owner task:"
)

var (
	eventLine = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2} (.*)$`)
	laneTag   = regexp.MustCompile(` \[lane ([a-z])\]$`)
	spawnRe   = regexp.MustCompile(`^spawned (\S+) \(([^)]*)\): (.*)$`)
	prRe      = regexp.MustCompile(`^pr #([0-9]+) (\S+) (\S+)$`)
	blockedRe = regexp.MustCompile(`^blocked (\S+?):`)
	idleRe    = regexp.MustCompile(`^idle (\S+?):`)
	readyRe   = regexp.MustCompile(`^ready #([0-9]+)`)
	pausedRe  = regexp.MustCompile(`^paused #([0-9]+)`)
	mergedRe  = regexp.MustCompile(`^merged #([0-9]+)`)
	ownerGoRe = regexp.MustCompile(`^owner go for #([0-9]+)`)
)

// Task is one worker of a state file with the state its last line set.
type Task struct {
	ID        model.TaskID
	Agent     string
	Lane      string
	Model     string
	Prompt    string
	OwnerTask bool
	PR        int
	Branch    string
	State     model.TaskState
	// Line is the state file line that set State.
	Line string
}

// TaskID returns the ID of the importer's task for a worker agent.
func TaskID(agent string) model.TaskID { return model.TaskID(IDPrefix + agent) }

// parser replays the lines of one state file.
type parser struct {
	worker *regexp.Regexp
	tasks  []*Task
	agents map[string]*Task
	prs    map[int]*Task
	// waiting marks the tasks a ready line moved to needs_owner; a new spawn keeps them.
	waiting map[*Task]bool
	// merging marks the tasks the owner gave the go; the lane's next spawn means they merged.
	merging map[*Task]bool
}

// ParseLog replays the state file of project label into its workers' tasks, in spawn order.
func ParseLog(label string, data []byte) ([]Task, error) {
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("%w: state file %s.md is not UTF-8; repair or move it out of the state directory",
			ErrUnreadable, label)
	}
	p := &parser{
		worker: regexp.MustCompile(`^` + regexp.QuoteMeta(strings.ToLower(label)) + `(?:-([a-z]))?-[0-9]+$`),
		agents: map[string]*Task{}, prs: map[int]*Task{}, waiting: map[*Task]bool{}, merging: map[*Task]bool{},
	}
	for line := range strings.Lines(string(data)) {
		line = strings.TrimRight(line, "\r\n")
		m := eventLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		lane, text := DefaultLane, m[1]
		if tag := laneTag.FindStringSubmatchIndex(text); tag != nil {
			lane, text = text[tag[2]:tag[3]], text[:tag[0]]
		}
		p.event(line, lane, text)
	}
	out := make([]Task, 0, len(p.tasks))
	for _, t := range p.tasks {
		out = append(out, *t)
	}
	return out, nil
}

func (p *parser) event(line, lane, text string) {
	if m := spawnRe.FindStringSubmatch(text); m != nil {
		p.spawn(line, m[1], m[2], m[3])
		return
	}
	if m := prRe.FindStringSubmatch(text); m != nil {
		if t := p.agents[m[2]]; t != nil && p.set(t, model.TaskGating, line) {
			t.PR, _ = strconv.Atoi(m[1])
			t.Branch = m[3]
			p.prs[t.PR] = t
		}
		return
	}
	for _, ev := range []struct {
		re    *regexp.Regexp
		state model.TaskState
	}{{blockedRe, model.TaskNeedsOwner}, {idleRe, model.TaskDone}} {
		if m := ev.re.FindStringSubmatch(text); m != nil {
			if t := p.agents[m[1]]; t != nil {
				p.set(t, ev.state, line)
			}
			return
		}
	}
	for _, ev := range []struct {
		re    *regexp.Regexp
		state model.TaskState
	}{{readyRe, model.TaskNeedsOwner}, {pausedRe, model.TaskPaused}, {ownerGoRe, model.TaskGating},
		{mergedRe, model.TaskDone}} {
		if m := ev.re.FindStringSubmatch(text); m != nil {
			pr, _ := strconv.Atoi(m[1])
			t := p.prs[pr]
			if t == nil && ev.re == mergedRe {
				t = p.laneWorker(lane, pr)
			}
			if t != nil && p.set(t, ev.state, line) {
				p.waiting[t] = ev.re == readyRe
				p.merging[t] = ev.re == ownerGoRe
			}
			return
		}
	}
}

// laneWorker gives pr to the lane's newest running or gating task without one (older files lack pr lines).
func (p *parser) laneWorker(lane string, pr int) *Task {
	for _, t := range slices.Backward(p.tasks) {
		if t.Lane == lane && t.PR == 0 && (t.State == model.TaskRunning || t.State == model.TaskGating) {
			t.PR = pr
			p.prs[pr] = t
			return t
		}
	}
	return nil
}

func (p *parser) spawn(line, agent, args, prompt string) {
	m := p.worker.FindStringSubmatch(agent)
	if m == nil || p.agents[agent] != nil {
		return
	}
	lane := m[1]
	if lane == "" {
		lane = DefaultLane
	}
	for _, t := range p.tasks {
		if t.Lane != lane {
			continue
		}
		switch {
		case p.merging[t]:
			p.set(t, model.TaskDone, line)
		case t.State == model.TaskRunning || t.State == model.TaskPaused ||
			(t.State == model.TaskNeedsOwner && !p.waiting[t]):
			p.set(t, model.TaskCanceled, line)
		}
	}
	mdl, _, _ := strings.Cut(args, ",")
	t := &Task{ID: TaskID(agent), Agent: agent, Lane: lane, Model: strings.TrimSpace(mdl), Prompt: prompt,
		OwnerTask: strings.HasPrefix(prompt, OwnerTaskPrefix), State: model.TaskRunning, Line: line}
	p.tasks = append(p.tasks, t)
	p.agents[agent] = t
}

// set moves t to state unless t is final; it reports whether it did.
func (p *parser) set(t *Task, state model.TaskState, line string) bool {
	if t.State == model.TaskDone || t.State == model.TaskCanceled {
		return false
	}
	t.State, t.Line = state, line
	p.waiting[t], p.merging[t] = false, false
	return true
}
