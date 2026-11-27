# ADR-0002: A clean base merge carries the review

Status: accepted (owner decision 2026-11-27)

## Context

D5 says a review counts only for the head it reviewed. Workers bring a branch up to date with
`git merge origin/main`, so every catch-up push produced a head without `fanin/review` and a second full review
run, although the branch's own changes were already reviewed and nothing in them changed.

## Options

- **A.** Review every head (D5 as written). Safe, but each catch-up costs a full review run and its wait.
- **B.** Carry the ready review of the first parent to a head that only merges the base into it, without
  conflicts. Saves the run; the combination of the reviewed changes with the new base is left to CI.
- **C.** Carry the review over any merge commit. Also skips merges with conflict resolutions or edits, which are
  code nobody reviewed.

## Decision

B. fanin carries the review only when the head is a two-parent merge, its first parent's newest `fanin/review`
is ready, its second parent is on `origin/<base>`, and `git merge-tree --write-tree` of the two parents gives the
head's own tree (no conflicts, no changes of its own). Anything else gets a run of its own.

## Consequences

- A catch-up merge is green once CI is green on it; CI still gates every head.
- A semantic clash between the reviewed changes and the new base is caught by CI only, not by the review.
- The check needs git 2.38 or newer (`merge-tree --write-tree`) in the project's clone.
