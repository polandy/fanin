---
name: pr-review
description: Quality review of a fanin pull request (docs and spec sync, CLAUDE.md rules, implementation, tests against scenario IDs, local CI, branch freshness). Posts the verdict as a PR comment. Use when asked to review a PR by number or branch, or "local diff, no PR yet" to review git diff origin/main...HEAD without posting.
argument-hint: <PR number or branch>
---

# PR Quality Review

You review a pull request for fanin. The PR is given in `$ARGUMENTS` (a number or branch; if omitted, use the PR of the current branch via `gh pr view`). If the argument says "local diff, no PR yet", review `git diff origin/main...HEAD`, post no comment and return the findings.

Work through all sections in order, collect findings, fix what the sections say to fix, and finish with the verdict. Do not wait for CI.

## 0. Gather context

- `gh pr view <PR> --json title,body,baseRefName,headRefName,mergeStateStatus,statusCheckRollup` and `gh pr diff <PR>`.
- Work in the PR's worktree (`../fanin-<slug>`) so you can build and test.
- Read the PR description, linked ADRs and specs first: the review checks the code against its stated intent.
- `CLAUDE.md` and `docs/dev/guidelines.md` are the standard. If they disagree with this skill, the files win. Never read or search `vendor/`.

## 1. Docs and spec sync

- Doc comments on exported symbols match the behaviour.
- A behaviour change starts in a spec under `docs/dev/` (`concept.md`, `scenarios.md`); the spec ships with the code.
- User-facing config, flags and operation changes appear in `docs/user/`. Docs must not describe behaviour the PR removed.
- Docs live only in `docs/user/` and `docs/dev/`.
- A finished roadmap item is removed from its todo file in the same PR.

## 2. ADRs

- Does the PR make a real tradeoff that needs a new ADR (`docs/dev/adr/NNNN-slug.md`, next free number on origin/main)?
- Does it contradict an existing ADR? Then the ADR needs a status update or the PR must change.

## 3. Implementation against CLAUDE.md

Flag new violations of the rules in `CLAUDE.md` §Code:

- errors not wrapped with `%w`, string-matched errors, `_ = f()` on an error that matters, messages that do not name key, value and fix;
- an unreadable gate, forge, herdr or store answer turned into a verdict instead of an error (fail closed);
- `ctx` not first or stored in a struct, goroutines outside `safego.Go`;
- magic strings or numbers (statuses, verdicts, event types, limits) that are not constants;
- external processes outside `runtime.Runner`, time outside the injected `Clock`, non-atomic file writes;
- config without `KnownFields(true)` or JSON Schema validation;
- an exported symbol without a doc comment, comments that are long or narrate the change;
- `slog` without `task_id`/`run_id`/`project`, or logging a token;
- dead code left for later;
- `go.mod` changed without `go mod tidy && go mod vendor`.

The repo is public: flag any private data (hostnames, IPs, absolute home paths, names, emails, secrets).

## 4. Tests

- Every new behaviour has a table test with `t.Run`; a bug fix has a test that fails without it; `test:` commit comes before the fix.
- Test names cite the scenario ID (`TestGate/GH-2_...`) and every scenario the PR covers exists in `docs/dev/scenarios.md` (`make scenario-check`).
- Failure paths are covered for gate, sched, store, hook, owner and every adapter.
- No `time.Sleep` or wall-clock waits; an absence assertion has a positive signal next to it.
- Real SQLite, never a mocked store; real processes only under `-tags integration`.
- Run `make ci` (it mirrors CI). A red step is a fix, never "flaky" and never re-run to get green: find the root cause.

## 5. CI status

- `gh pr checks <PR>`; read failures with `gh run view --log-failed`, fix them on the branch, run `make ci`, commit with a Conventional Commit (feat|fix|docs|chore|refactor|test|ci), push.

## 6. Branch freshness

- `git fetch origin && git rev-list --count HEAD..origin/main`. If behind, `git merge origin/main` and push normally. Never rebase, never force-push. After a merge, re-run sections 1-5.

## 7. Verdict

1. **Summary**: what the PR does.
2. **Findings** per section: ok / issue (file:line) / fixed by me (commit).
3. **Blockers**: what must change and you could not fix.
4. **Merge readiness**: ready / not ready. Never merge: the owner merges (squash, hand-written Conventional Commit, no attribution lines).

## 8. Post the verdict

After the last push, post the verdict as a comment on the PR:

```
gh pr comment <PR> --body '<verdict, GitHub-flavored markdown>'
```

- English. Start with `## PR quality review` and use the structure of section 7.
- Name the head commit the review covers.
- If an earlier comment from this skill exists, replace it (`gh pr comment --edit-last`) instead of stacking.
