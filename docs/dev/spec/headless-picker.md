# Headless runtime and picker spec

`internal/runtime/headless` runs one short `claude -p` session to completion; `internal/pick` builds on it to choose
the next task. Both go through `runtime.Runner`, every wait through the injected `Clock`. The headless runtime is
synchronous (`Run`, no pane to observe), so it does not implement the herdr-shaped `WorkerRuntime`; opencode joins it
later as another argv behind the same `Run`.

## Headless run

`Run(ctx, Spec) (Result, error)` starts `claude -p --output-format json --model <model> [--allowedTools <a,b>]` in
`Spec.Dir` with the prompt on stdin. The JSON answer gives `Result.Output` (the final text) and `Result.Usage`
(input, output, cache creation and cache read tokens).

- `Spec.Env` is added to the process environment, such as the review's `GIT_LFS_SKIP_SMUDGE=1`.
- `Spec.Timeout` is armed on the Clock; past it the process is killed and `Run` returns `ErrTimeout` with the output tail.
- A non-zero exit is the runner's `*runtime.ExitError`; unparsable JSON, `is_error` or an empty result is `runtime.ErrOddAnswer`.
- A spec without model, prompt or a positive timeout is `runtime.ErrInvalidSpec`; nothing is started.

## Picker

`Picker.Pick(ctx, Request) (Result, error)` runs the picker with the model and timeout of `pick.Config` (the picker
model comes from the worker kind's config; default timeout 600 s, PK-1).

| Forge | Allowed tools | In-progress rule |
|---|---|---|
| github | `Read,Glob,Grep,Bash(gh pr list:*),Bash(gh issue list:*)` | `gh pr list` / `gh issue list` (PK-1) |
| gitea | `Read,Glob,Grep` | the request's in-progress list; the project's base branch (PK-2) |

The prompt names the project, the base branch, the in-progress rule and the answer contract. The last non-empty line
of the output decides:

- `PICK sonnet|opus: <task>` sets task and model (PK-1).
- `NOTHING: <reason>` is an idle result; no worker starts (PK-3).
- Anything else (junk, an unknown model, a PICK inside a line or before the last line, no task, a timeout, a failed
  run) is `ErrBadPick` with the output tail, never a pick (PK-4). The caller logs "pick failed" and spawns a worker
  that chooses its own task.
