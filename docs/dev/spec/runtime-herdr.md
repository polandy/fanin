# herdr runtime spec

`internal/runtime` defines the `WorkerRuntime` interface (concept D2); `internal/runtime/herdr` is its first
adapter. Every herdr call goes through `runtime.Runner`, every wait through the injected `Clock`.

## Interface

| Method | Does |
|---|---|
| `Start(ctx, RunSpec) (RunHandle, error)` | workspace, tab, agent start, prompt; returns once the agent works |
| `Prompt(ctx, RunHandle, text) error` | sends text; a refusal is an error at once (SP-5, AW-13) |
| `Observe(ctx, RunHandle) (<-chan RunEvent, error)` | polls herdr and the pane and streams `RunEvent`s (see Observe) |
| `Stop(ctx, RunHandle) error` | closes the run's pane, never its tab (FP-7, MG-7) |

`RunSpec` carries the task and run IDs (for logs), the project label, host, worktree, agent name, short title, model, prompt and MCP config path, plus the worker kind (`claude` or
`opencode`; empty is claude), the base branch and a directory to put first on the pane's PATH.
The session name is `<project> · <short title> · <agent>` (SP-1); the title is left out when empty. Agent names
are slot names without a lane (C-SLOTS). `RunHandle` names host, agent, workspace, tab and pane, plus the task and run IDs and the project for logs.

Errors are sentinels in `internal/runtime`, matched with `errors.Is`: `ErrInvalidSpec`, `ErrOddAnswer`,
`ErrStartFailed`, `ErrAgentTaken`, `ErrTrustDialog`, `ErrPromptRefused`, `ErrNotWorking`, `ErrStatusUnknown`,
`ErrUnreachable`.
Each wraps herdr's `*runtime.ExitError` where there is one, and names the agent, pane and tab.

## herdr commands

Taken from the bash orchestrator's `spawn.sh`, `lib.sh` and `finish.sh`, which drive the herdr in use today.
A run on another host prefixes every command with `herdr --machine <host>`. Every answer is JSON on stdout.

| Step | argv after `herdr` | Answer read |
|---|---|---|
| find workspace | `workspace list` | `.result.workspaces[] \| {label, workspace_id}` |
| create workspace | `workspace create --cwd <worktree> --label <project> --no-focus` | `.result.workspace.workspace_id` |
| new tab | `tab create --workspace <ws> --cwd <worktree> --label <agent> --no-focus` | `.result.tab.tab_id`, `.result.root_pane.pane_id` |
| PATH | `pane run <pane> 'set -gx PATH <dir> $PATH'`, only with a PATH directory | exit code |
| start | `agent start <agent> --pane <pane> --timeout <ms> --kind claude -- --permission-mode auto --strict-mcp-config --mcp-config <file> [--model <m>] --name <session>` | exit code |
| start (opencode) | `agent start <agent> --pane <pane> --timeout <ms> --kind opencode -- <project>` | exit code |
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
2. Wait `ShellPause` (5 s) for the pane's shell, then put the PATH directory first when the spec has one.
3. `agent start`. On failure, ask for the agent's status:
   - listed in this pane: started, go on (SP-3);
   - listed in another pane: `ErrAgentTaken`, no prompt (SP-3);
   - gone: read 30 pane lines; claude's `trust this folder` dialog is `ErrTrustDialog`, naming pane and tab
     (SP-4); else retry after `StartInterval`, up to `StartTries` tries. The last failure is `ErrStartFailed`
     with herdr's last error; the tab stays open for the owner (SP-2).
4. `agent prompt`; a refusal is `ErrPromptRefused` with herdr's reason and the pane (SP-5). A tab or workspace
   answer without its ids is `ErrOddAnswer`, before any start (SP-5).
5. Up to `WorkChecks` times, every `WorkCheckInterval`: `working` or `blocked` ends the start. Otherwise read 6
   pane lines; an empty input box (claude: `❯` alone or with its `Try "` hint; opencode: `┃  Ask anything`) gets the prompt again, any other
   box gets Enter (SP-6). After the last check it is `ErrNotWorking` with the status.

A failed pane read, resend or Enter is an error, not a skipped step.

## Observe (AW-3 to AW-9, AW-12, WA-3)

The pane text is read only to spot dialogs, stalls and live background work, never for the result (C-REPORT):
the result comes from `fanin_report` or `result.json`.

Every `PollInterval` one probe runs `agent get` and `pane read <pane> --lines 200`. Its events:

| Event | When | Carries |
|---|---|---|
| `working` | the agent starts working | |
| `idle` | herdr says `idle` or `done`, once per stop | `LastWords`, `Tail` |
| `gone` | herdr no longer lists the agent; the channel closes, the pane is not read (AW-7) | |
| `dialog` | herdr says `blocked`; again when the dialog text changes | `Dialog` |
| `stalled` | see Stalls | `Reason`, `LastWords`, `Tail` |
| `answered_in_pane` | the agent leaves a dialog, or goes from idle to working, and fanin sent it no prompt or Enter since it began waiting (WA-3) | |
| `unreachable` | a read stayed failed after every retry, or an Enter failed; the channel closes | `Err` |

Every event carries herdr's `Status` and the clock time `At`. The channel also closes when `ctx` ends.

- **Reads (AW-6, AW-7).** A failed `agent get` (with its `agent list`), a failed `pane read` and an empty pane
  are re-read up to `ReadRetries` times, `ReadRetryInterval` apart. A failed read is never a sample, so a hiccup
  between two samples neither stalls nor settles the run. After the last retry the event is `unreachable`, wrapping
  `ErrUnreachable` and herdr's error. A status word other than `working`, `blocked`, `idle` or `done` is an odd
  answer and goes the same way.
- **Dialogs (AW-9).** The bottom `DialogLines` (30) are searched for numbered options counted from 1, at least two,
  one of them behind claude's `❯` cursor. The question is the nearest text line above the first option.
  `Dialog` is `<question> 1. <option> / 2. <option>`, cut to 400 runes. A blocked pane without such options is
  `unreadable`. A numbered list in a reply has no cursor and is no dialog.
- **Background work (AW-3).** A footer in the bottom `FooterLines` (4) that counts background tasks, agents or
  shells holds an idle agent: no `idle` is sent for up to `MaxBGWaits` (50) slices of `StallSlice`. The `idle`
  sent once the hold is over carries `BGFooter`, so the scheduler waits again (AW-11). A footer further up the
  scrollback does not count.
- **Stalls (AW-4, AW-5).** The pane fingerprint drops the spinner line and timers and token counts. A working
  agent whose fingerprint stays the same for `StallSlices` (3) times `StallSlice` (300 s) is `stalled` with reason
  `quiet`; an idle agent held by a live shell footer whose tail stays the same for `BGShellWindow` (20 min) is
  `stalled` with reason `bg-shell`. Real output restarts the window, and each stall starts the next one. The
  daemon takes a missing `fanin_progress` heartbeat as the stall signal; the fingerprint is the fallback for
  workers without MCP.
- **Resume (AW-8).** Within `ResumeGrace` (120 s) of Observe's start, text left in an idle agent's input box gets
  Enter, at most `ResumeEnters` (2) times, and no `idle` is sent while the grace lasts.
- **Last words (AW-12).** `LastWords` is the last 5 non-empty lines above the input box, so the box and the footer
  are dropped. `Tail` is the whole pane read; the daemon stores it per run.

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
| `PollInterval` | 30 s between probes | AW-3 to AW-9 |
| `ReadRetries` | 3 | AW-6 |
| `ReadRetryInterval` | 30 s | AW-6 |
| `StallSlice` | 300 s | AW-3, AW-4 |
| `StallSlices` | 3 | AW-4 |
| `BGShellWindow` | 20 min | AW-5 |
| `MaxBGWaits` | 50 slices | AW-3 |
| `ResumeEnters` | 2 | AW-8 |
| `ResumeGrace` | 120 s | AW-8 |

They are not in the YAML config yet; the daemon wiring adds them when it starts workers.

## Opencode workers (SP-9)

`internal/runtime/opencode` wraps the herdr adapter. A claude spec passes through; any kind but claude and
opencode is `ErrInvalidSpec`. For an opencode spec, before any herdr call:

1. `sudo -n -u aiagent git -C /srv/aiagent/<project> status --porcelain`; any line is `opencode.ErrDirtyClone`
   (`<clone> is not clean`, the first 5 lines, and the hint to move the leftovers out, never delete them).
2. `git -C <worktree> fetch -q origin <base>`, then `git bundle create -q - refs/remotes/origin/<base>`.
3. The bundle goes on stdin to `sudo -n -u aiagent sh -c <SyncScript> sh <clone> <base>`, which writes it beside
   the clone and fetches it into `refs/remotes/origin/<base>`: the sandbox has no network.
4. The shim `<state dir>/oc-bin/opencode` (`exec /usr/local/bin/oc "$@"`) is written atomically, mode 0755.
5. herdr starts `--kind opencode` with the shim directory on PATH; the oc prompt follows as for claude.

A sudo password prompt is an error that names the missing NOPASSWD sudoers line. `fanind` always wraps the herdr
runtime this way; the scheduler only sends opencode specs for GitHub PR tasks (see [`sched.md`](sched.md)).

## Not yet

Remote Control names come with a later work package.
