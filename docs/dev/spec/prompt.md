# Prompt spec

`internal/prompt` renders the worker prompts (SCENARIOS SP-7), the nudges (AW-10, AW-11) and a follow-up run's
handover ([handover spec](handover.md)). The templates are embedded Markdown under
`internal/prompt/templates/`, ported from the bash orchestrator's `prompts/` and cleaned of host names, paths and
people.

## Templates

| Name | For |
|---|---|
| `worker` | a worker with forge access: task to reviewed PR |
| `worker-oc` | a sandboxed worker without network: task to a commit |
| `worker-gitea` | a worker without forge access in a repo that deploys: task to a pushed branch |
| `worker-analyze` | an analysis question, answered as a docs PR or an owner-decision issue |
| `worker-gitea-analyze` | the analysis variant without forge access |

Shared text lives in `templates/parts/` (`owner-input`, `no-herdr-no-ssh`, `hard-limits-hosts`, `status-line`).

The nudges (`prompt.Nudges`) are templates of their own: `nudge-stall` (no progress), `nudge-continue`
(NUDGE_CONTINUE, a stop without a result), `nudge-secure` (NUDGE_SECURE, push the unpushed commits),
`nudge-regate` (HINT, the re-gate before the merge was not green), `nudge-fix` (HINT, CI is red),
`nudge-review` (HINT, the review is red), `nudge-conflict` (HINT and BASE, merge `origin/<base>` in, never
rebase) and `nudge-ci-fixed` (BASE, the base branch is
green again; merge it in).

## Rendering

`prompt.Render(name, Vars{Repo, Base, Hint, Handover})`:

1. `{{INCLUDE:part}}` is replaced by `parts/<part>.md`, repeatedly (at most 16 rounds, so a cycle is an error).
2. Any remaining placeholder other than `{{REPO}}`, `{{BASE}}`, `{{HINT}}` is `ErrPlaceholder`.
3. The three values go in in one pass, so a hint is inserted literally: a `{{REPO}}` or `{{INCLUDE:x}}` in it stays as typed.

A missing template or part is `ErrTemplate` (wrapping `fs.ErrNotExist`); the renderer never returns a prompt with an
include left in. The output ends with one newline. A non-empty `Handover` (`Handover.Markdown()`) is appended
after one blank line, as it is: nothing in it is expanded.

## Report instruction

The bash status-line part becomes the `status-line` part: before finishing, call the MCP tool
`fanin_report` (`faninmcp.ToolReport`) once, then end with the one-line format of the template. The instruction is the
line right before the format line and appears once. The worker prompt asks for a review of the local diff
(`/pr-review local diff, no PR yet`) before the first push and not for a PR-level `/pr-review`: fanind's
`fanin/review` is the only gate review.

Golden files in `internal/prompt/testdata` pin each rendered template (`go test ./internal/prompt -update`).
