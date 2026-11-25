# herdr runtime spec

`internal/runtime` defines the `WorkerRuntime` interface (concept D2); `internal/runtime/herdr` is its first
adapter. Every herdr call goes through `runtime.Runner`, every wait through the injected `Clock`.

## Interface

| Method | Does |
|---|---|
| `Start(ctx, RunSpec) (RunHandle, error)` | workspace, tab, agent start, prompt; returns once the agent works |
| `Prompt(ctx, RunHandle, text) error` | sends text; a refusal is an error at once (SP-5, AW-13) |
| `Observe(ctx, RunHandle) (<-chan RunEvent, error)` | stub: the channel sends nothing and closes with `ctx` |
| `Stop(ctx, RunHandle) error` | closes the run's pane, never its tab (FP-7, MG-7) |

`RunSpec` carries the project label, host, worktree, agent name, short title, model, prompt and MCP config path.
The session name is `<project> · <short title> · <agent>` (SP-1); the title is left out when empty. Agent names
are slot names without a lane (C-SLOTS). `RunHandle` names host, agent, workspace, tab and pane.

Errors are sentinels in `internal/runtime`, matched with `errors.Is`: `ErrInvalidSpec`, `ErrOddAnswer`,
`ErrStartFailed`, `ErrAgentTaken`, `ErrTrustDialog`, `ErrPromptRefused`, `ErrNotWorking`, `ErrStatusUnknown`.
Each wraps herdr's `*runtime.ExitError` where there is one, and names the agent, pane and tab.

## herdr commands

Taken from the bash orchestrator's `spawn.sh`, `lib.sh` and `finish.sh`, which drive the herdr in use today.
A run on another host prefixes every command with `herdr --machine <host>`. Every answer is JSON on stdout.

| Step | argv after `herdr` | Answer read |
|---|---|---|
| find workspace | `workspace list` | `.result.workspaces[] \| {label, workspace_id}` |
| create workspace | `workspace create --cwd <worktree> --label <project> --no-focus` | `.result.workspace.workspace_id` |
| new tab | `tab create --workspace <ws> --cwd <worktree> --label <agent> --no-focus` | `.result.tab.tab_id`, `.result.root_pane.pane_id` |
| start | `agent start <agent> --pane <pane> --timeout <ms> --kind claude -- --permission-mode auto --strict-mcp-config --mcp-config <file> [--model <m>] --name <session>` | exit code |
| status | `agent get <agent>` | `.result.agent.agent_status`, `.result.agent.pane_id` |
| listed? | `agent list` | `.result.agents[].name` |
| pane text | `pane read <pane> --lines <n>` | plain text |
| prompt | `agent prompt <agent> <text>` | exit code |
| Enter | `agent send-keys <agent> enter` | exit code |
| close | `pane close <pane>` | exit code |

herdr reports `working`, `blocked`, `idle` and `done`. The adapter adds `gone` for an agent that `agent get`
cannot read and `agent list` does not list. A failing or unreadable `agent list`, or an agent that is listed but
has no readable status, is `ErrStatusUnknown` (SP-11): never a verdict, and never followed by a second start.

## Start (SP-1 to SP-6)

1. Reuse the workspace labelled with the project, else create it. Open a new tab labelled with the agent.
2. Wait `ShellPause` (5 s) for the pane's shell.
3. `agent start`. On failure, ask for the agent's status:
   - listed in this pane: started, go on (SP-3);
   - listed in another pane: `ErrAgentTaken`, no prompt (SP-3);
   - gone: read 30 pane lines; claude's `trust this folder` dialog is `ErrTrustDialog`, naming pane and tab
     (SP-4); else retry after `StartInterval`, up to `StartTries` tries. The last failure is `ErrStartFailed`
     with herdr's last error; the tab stays open for the owner (SP-2).
4. `agent prompt`; a refusal is `ErrPromptRefused` with herdr's reason and the pane (SP-5). A tab or workspace
   answer without its ids is `ErrOddAnswer`, before any start (SP-5).
5. Up to `WorkChecks` times, every `WorkCheckInterval`: `working` or `blocked` ends the start. Otherwise read 6
   pane lines; an empty claude input box (`❯` alone or with its `Try "` hint) gets the prompt again, any other
   box gets Enter (SP-6). After the last check it is `ErrNotWorking` with the status.

A failed pane read, resend or Enter is an error, not a skipped step.

## Limits

`herdr.Config`, defaults from `DefaultConfig()`:

| Field | Default | Scenario |
|---|---|---|
| `StartTries` | 8 | SP-2 |
| `StartTimeout` | 120 s, passed as `--timeout` in ms | SP-2 |
| `StartInterval` | 120 s between tries | SP-2 |
| `ShellPause` | 5 s | SP-1 |
| `WorkChecks` | 10 | SP-6 |
| `WorkCheckInterval` | 3 s | SP-6 |

They are not in the YAML config yet; the daemon wiring adds them when it starts workers.

## Not yet

Observe (pane reads for dialogs and stalls), Remote Control names and opencode workers (SP-9) come with later
work packages.
