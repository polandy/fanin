# Review spec

`internal/review` runs a project's review skill headless on a PR head and posts its verdict as the commit status
`fanin/review` (`gate.ReviewContext`) on that head (concept D5). Branch protection requires the status like CI;
the gate reads it ([`gate.md`](gate.md)). An unreadable review is an error, never a verdict.

## Settings

| Key | Default | Use |
|---|---|---|
| `review_skill` | `/pr-review` | skill or command the run starts with, followed by the PR number |
| `review_model` | `sonnet` | model of the headless run |
| `review_timeout` | `30m` | longest run (OC-3); past it the run is killed and `headless.ErrTimeout` returned |

The defaults come from the config schema; `review.Config` without a skill, a model or a positive timeout is
`runtime.ErrInvalidSpec` before anything runs. The run is a headless `claude -p` (`internal/runtime/headless`);
the opencode review of OC-4 is `review.Opencode` (see below).

Only GitHub projects are reviewed for now: the skill posts its comment with `gh`, and fanin reads it back through
`forge.Comments`, which the Gitea client does not have.

## The run (SCENARIOS OC-3)

1. Read the PR. It must be open (`forge.ErrNotOpen`), its head a hex SHA and its head branch a name git cannot
   read as a flag; otherwise `review.ErrUnreadable` and git never runs.
2. Read the PR's comments as the baseline.
3. `git -C <dir> fetch -q origin <branch>`, then
   `git -C <dir> worktree add -q --detach <dir>/.claude/worktrees/fanin-review-<n> <head sha>` with
   `GIT_LFS_SKIP_SMUDGE=1`. All git goes through `runtime.Runner`.
4. Run `claude -p` in the worktree with the prompt `<skill> <n>` plus fanin's rules: review only, the head SHA, the
   comment contract, change nothing. The tools are read-only (`Read`, `Grep`, `Glob`, `git diff/log/show/status`,
   `gh pr view/diff/checks`) plus `gh pr comment`.
5. Read the comments again. The newest comment that is new or changed against the baseline is the review; none is
   `review.ErrNoComment`.
6. Parse the verdict and post `fanin/review` on the head SHA the worktree had: `success` with `ready`, or
   `failure` with `not ready: see the review comment`. The target URL is the comment.
7. Remove the worktree with `git worktree remove --force`, also on error and on a cancelled context.
8. Record the summed token usage as a `token_usage` event with counts only, never the review's text.

`Result.Text` is the canonical review on one line, cut to 1500 characters. On any error nothing is posted: the
head keeps no review and the gate stays RED `no review for head <sha>`. A push after the review moves the head,
and the new head has no status until it is reviewed again (D5).

## The verdict (SCENARIOS OC-5)

The comment's first line is `## PR quality review`, its verdict a line `Merge readiness: Ready` or
`Merge readiness: Not ready`. The parser accepts a preamble before the header, code fences, bold or other casing,
a verdict on the line after the label, and reworded verdicts (`Ready to merge.`, `NOT READY`). The last verdict
wins; a line naming both (`ready / not ready`) is Not ready.

A comment without the header or without a verdict gets one retry with a correction note naming what was missing.
A second failure is `review.ErrUnreadable`. A run with no text is `runtime.ErrOddAnswer`; a run that stopped at a
sudo password prompt is `review.ErrSudoPassword`, whose message names the fix.

## The opencode review (SCENARIOS OC-4)

`review.Opencode` reviews an opencode PR read-only in the sandbox; opencode answers, fanin posts.

1. The model is `OpencodeConfig.Model`, else `classes["cloud-fallback"].model` of the LLM policy file; an
   unreadable policy is an error before anything runs.
2. Read the PR as above, then `git -C <dir> fetch -q origin <base> <branch>` and
   `git -C <dir> diff origin/<base>...<head sha>`.
3. `sudo -n -u aiagent oc-sandbox <label> --script` with `git rev-parse HEAD && git status --porcelain` on stdin:
   the clone must stand at the PR head with no changes, else `review.ErrCloneState` and nothing is posted.
4. `timeout 1800 sudo -n -u aiagent oc-sandbox <label> --run --model <m> --title "review PR <n>"` with the prompt
   on stdin: fanin's rules (the comment contract, change nothing) and the diff, cut at 60000 bytes
   (`review.DiffMax`) on a character boundary with the note `only the first 60000 of <n> bytes`.
5. The output is opencode's JSON event lines; the `text` parts are the review. A line that is not JSON, or no
   text, is `review.ErrUnreadable`. The clone state is checked again after every run.
6. Parse as in The verdict, one retry with the correction note. Post the canonical text plus the model footer
   (`review.OCFooter`) with `gh pr comment --body-file -`, then the `fanin/review` status linking that comment.
   A failed comment post posts no status.
