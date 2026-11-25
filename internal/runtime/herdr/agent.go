package herdr

import (
	"context"
	"fmt"

	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/safego"
)

// agentState is what herdr says about one agent.
type agentState struct {
	Status string
	Pane   string
}

// agentState asks herdr about agent; StatusGone when herdr no longer lists it, ErrStatusUnknown when herdr cannot say.
func (r *Runtime) agentState(ctx context.Context, host, agent string) (agentState, error) {
	out, getErr := r.herdr(ctx, host, "agent", "get", agent)
	if getErr == nil {
		var got struct {
			Result struct {
				Agent struct {
					AgentStatus string `json:"agent_status"`
					PaneID      string `json:"pane_id"`
				} `json:"agent"`
			} `json:"result"`
		}
		if err := decode(out, &got); err == nil && got.Result.Agent.AgentStatus != "" {
			return agentState{Status: got.Result.Agent.AgentStatus, Pane: got.Result.Agent.PaneID}, nil
		}
		getErr = fmt.Errorf("%w: herdr agent get %s: %s", runtime.ErrOddAnswer, agent, tail(out))
	}
	out, err := r.herdr(ctx, host, "agent", "list")
	if err != nil {
		return agentState{}, unknown(agent, err)
	}
	var list struct {
		Result struct {
			Agents *[]struct {
				Name string `json:"name"`
			} `json:"agents"`
		} `json:"result"`
	}
	if err := decode(out, &list); err != nil {
		return agentState{}, unknown(agent, err)
	}
	if list.Result.Agents == nil {
		return agentState{}, unknown(agent, fmt.Errorf("%w: no agents in %s", runtime.ErrOddAnswer, tail(out)))
	}
	for _, a := range *list.Result.Agents {
		if a.Name == agent {
			return agentState{}, unknown(agent, fmt.Errorf("listed, but agent get failed: %w", getErr))
		}
	}
	return agentState{Status: StatusGone}, nil
}

func unknown(agent string, err error) error {
	return fmt.Errorf("%w: status of %s unknown, no second worker started; check herdr: %w",
		runtime.ErrStatusUnknown, agent, err)
}

// readPane returns the last lines of pane's text.
func (r *Runtime) readPane(ctx context.Context, host, pane string, lines int) (string, error) {
	out, err := r.herdr(ctx, host, "pane", "read", pane, "--lines", fmt.Sprint(lines))
	if err != nil {
		return "", fmt.Errorf("cannot read pane %s: %w", pane, err)
	}
	return string(out), nil
}

// Prompt sends text to the run's agent; a refusal is an error at once, never retried (SCENARIOS SP-5, AW-13).
func (r *Runtime) Prompt(ctx context.Context, h runtime.RunHandle, text string) error {
	if _, err := r.herdr(ctx, h.Host, "agent", "prompt", h.Agent, text); err != nil {
		return fmt.Errorf("%w: prompt to %s (pane %s) failed: %w", runtime.ErrPromptRefused, h.Agent, h.Pane, err)
	}
	return nil
}

// Observe is a stub until the observer lands: its channel sends nothing and closes when ctx ends.
func (r *Runtime) Observe(ctx context.Context, _ runtime.RunHandle) (<-chan runtime.RunEvent, error) {
	ch := make(chan runtime.RunEvent)
	safego.Go(ctx, r.logger, func(ctx context.Context) {
		<-ctx.Done()
		close(ch)
	})
	return ch, nil
}

// Stop closes the run's pane, never its tab: the tab may hold other panes (SCENARIOS FP-7, MG-7).
func (r *Runtime) Stop(ctx context.Context, h runtime.RunHandle) error {
	if h.Pane == "" {
		return fmt.Errorf("%w: RunHandle.Pane is empty for agent %q; close its pane by hand", runtime.ErrInvalidSpec, h.Agent)
	}
	if _, err := r.herdr(ctx, h.Host, "pane", "close", h.Pane); err != nil {
		return fmt.Errorf("cannot close pane %s of %s: %w", h.Pane, h.Agent, err)
	}
	return nil
}
