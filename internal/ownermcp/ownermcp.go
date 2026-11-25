// Package ownermcp is the owner's MCP server: an agent session answers questions, releases tasks and reads the
// status through the same daemon API client the fanin CLI uses.
package ownermcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/polandy/fanin/internal/api/client"
	"github.com/polandy/fanin/internal/api/wire"
)

// Tool names of the owner server.
const (
	ToolQuestions = "fanin_questions"
	ToolAnswer    = "fanin_answer"
	ToolGo        = "fanin_go"
	ToolStatus    = "fanin_status"
)

// ServerName is the implementation name the server announces.
const ServerName = "fanin-owner"

// TaskInput is the input of the tools that act on one task.
type TaskInput struct {
	Task string `json:"task" jsonschema:"task id, e.g. the task_id of a question"`
}

// AnswerInput is the input of fanin_answer.
type AnswerInput struct {
	Task   string `json:"task" jsonschema:"task id of the open question"`
	Answer string `json:"answer" jsonschema:"option number 1-9, or free text when the question allows text"`
}

// StatusInput is the input of fanin_status.
type StatusInput struct {
	Project string `json:"project,omitempty" jsonschema:"limit the answer to one project; default all projects"`
}

// QuestionsInput is the input of fanin_questions; it takes no arguments.
type QuestionsInput struct{}

type server struct {
	c       *client.Client
	baseURL string
}

// New returns the owner MCP server. Every tool calls the daemon at baseURL through c; a failed call is a tool
// error, never a success message.
func New(c *client.Client, baseURL, version string) *mcp.Server {
	s := &server{c: c, baseURL: baseURL}
	srv := mcp.NewServer(&mcp.Implementation{Name: ServerName, Version: version}, nil)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        ToolQuestions,
		Description: "List the open owner questions, oldest first, with their options and the recommended one.",
	}, s.questions)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        ToolAnswer,
		Description: "Answer the open question of a task. An option number must name one of the question's options; an invalid answer is refused with the options and nothing is sent.",
	}, s.answer)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        ToolGo,
		Description: "Release a ready task (state gating) for merge.",
	}, s.goTask)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        ToolStatus,
		Description: "Read what is open: tasks, runs, questions, holds, project locks and the newest event per project.",
	}, s.status)
	return srv
}

func (s *server) questions(ctx context.Context, _ *mcp.CallToolRequest, _ QuestionsInput) (*mcp.CallToolResult, any, error) {
	qs, err := s.c.Questions(ctx)
	if err != nil {
		return s.fail(ToolQuestions, err), nil, nil
	}
	return jsonResult(ToolQuestions, qs)
}

func (s *server) answer(ctx context.Context, _ *mcp.CallToolRequest, in AnswerInput) (*mcp.CallToolResult, any, error) {
	if in.Task == "" || in.Answer == "" {
		return failText("%s needs task and answer; pass both", ToolAnswer), nil, nil
	}
	ack, err := s.c.Answer(ctx, in.Task, in.Answer)
	if err != nil {
		return s.fail(ToolAnswer+" "+in.Task, err), nil, nil
	}
	return textResult(in.Task + ": " + ack.Detail), nil, nil
}

func (s *server) goTask(ctx context.Context, _ *mcp.CallToolRequest, in TaskInput) (*mcp.CallToolResult, any, error) {
	if in.Task == "" {
		return failText("%s needs a task; pass its id", ToolGo), nil, nil
	}
	ack, err := s.c.Go(ctx, in.Task)
	if err != nil {
		return s.fail(ToolGo+" "+in.Task, err), nil, nil
	}
	return textResult(in.Task + ": " + ack.Detail), nil, nil
}

func (s *server) status(ctx context.Context, _ *mcp.CallToolRequest, in StatusInput) (*mcp.CallToolResult, any, error) {
	snap, err := s.c.Snapshot(ctx)
	if err != nil {
		return s.fail(ToolStatus, err), nil, nil
	}
	if in.Project != "" {
		if !slices.Contains(snap.Projects, in.Project) {
			return failText("%s: project %q is not in the config or the store; known: %s",
				ToolStatus, in.Project, strings.Join(snap.Projects, ", ")), nil, nil
		}
		snap = forProject(snap, in.Project)
	}
	return jsonResult(ToolStatus, snap)
}

// forProject keeps only what belongs to project.
func forProject(snap wire.Snapshot, project string) wire.Snapshot {
	out := wire.Snapshot{Projects: []string{project}, LastSeq: snap.LastSeq}
	tasks := map[string]bool{}
	for _, t := range snap.Tasks {
		if t.Project == project {
			tasks[t.ID] = true
			out.Tasks = append(out.Tasks, t)
		}
	}
	for _, r := range snap.Runs {
		if tasks[r.TaskID] {
			out.Runs = append(out.Runs, r)
		}
	}
	for _, q := range snap.Questions {
		if q.Project == project {
			out.Questions = append(out.Questions, q)
		}
	}
	for _, h := range snap.Holds {
		if h.Project == project {
			out.Holds = append(out.Holds, h)
		}
	}
	for _, l := range snap.Locks {
		if l.Project == project {
			out.Locks = append(out.Locks, l)
		}
	}
	for _, ev := range snap.Latest {
		if ev.Project == project {
			out.Latest = append(out.Latest, ev)
		}
	}
	return out
}

// fail turns a failed daemon call into a tool error that names the call, the cause and, for a connection
// failure, the daemon URL.
func (s *server) fail(what string, err error) *mcp.CallToolResult {
	var apiErr *client.Error
	if errors.As(err, &apiErr) {
		return failText("%s: %v", what, err)
	}
	return failText("%s: cannot reach fanind at %s: %v; start the daemon or fix FANIN_URL", what, s.baseURL, err)
}

func failText(format string, a ...any) *mcp.CallToolResult {
	res := textResult(fmt.Sprintf(format, a...))
	res.IsError = true
	return res
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func jsonResult(what string, v any) (*mcp.CallToolResult, any, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return failText("%s: encode the answer: %v", what, err), nil, nil
	}
	return textResult(string(b)), nil, nil
}
