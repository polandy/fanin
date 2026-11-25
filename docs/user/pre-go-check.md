# Pre-go check

A project can set `pre_go_check` (see [Configuration](configuration.md)): a command that fanind runs on a PR
before it asks for your go. Its output ends the `ready` line, so the question you answer shows what the merge
would do. fanin ships no check; you write one for your project (for example a dry run of a deploy, or a summary
of firewall, port and `network_mode: host` changes in the diff).

## When it runs

The check runs when a PR that waits for the owner's go (`ready #<n>`) has passed the gate, and only then. A PR
that merges without the owner does not run it. A go that arrives while the check runs merges after the check ends
with exit 0, never before.

## What fanind does

1. `git fetch origin <base> <branch>` in the project's checkout.
2. `git worktree add --force --detach <state dir>/prego/<project>-<pr> FETCH_HEAD`: a throwaway worktree of the PR's head.
3. `sh -c <pre_go_check>` with the worktree as working directory.
4. `git worktree remove --force` of the worktree, whatever the check did.

## Interface

Environment, on top of fanind's own:

| Variable | Value |
| --- | --- |
| `FANIN_PROJECT` | project name |
| `FANIN_PR` | PR number |
| `FANIN_BASE` | base branch name, e.g. `main` |
| `FANIN_BRANCH` | the PR's head branch |
| `FANIN_BASE_REF` | `origin/<base>`, fetched just before; diff against it, e.g. `git diff "$FANIN_BASE_REF"...HEAD` |

Exit codes:

- `0`: the check ran; its stdout goes on the ready line.
- anything else: the check failed. The task goes to `needs_owner` with a question that names the PR and the last
  line of stderr (else stdout). The PR is not merged and no go is asked for; fix the check or the PR, then
  answer. A fetch or worktree failure, a missing `PreGo` runner or an unknown branch is treated the same way: an
  unreadable check is never an all-clear.

Output: stdout only. Non-empty lines are trimmed and joined with `; `, and the result is cut at 600 characters.
Keep it to one or two short lines, for example:

```
dry-activate: firewall: no port or firewall rule changes
would: restart nginx
```

gives

```
ready #42: GREEN code <title>; <gate note>; dry-activate: firewall: no port or firewall rule changes; would: restart nginx
```

An empty output leaves the ready line as it is. There is no timeout beyond the daemon's own shutdown, so keep the
check quick.
