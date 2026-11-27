# fanin acceptance scenarios

Today's behaviour of the bash orchestrator that fanin replaces (a private repo, frozen for bug fixes only), written
as the scenarios fanin must pass. Derived from `.claude/skills/orchestrate/{scripts,tests,prompts}`, `SKILL.md`,
`CLAUDE.md` and `.claude/settings.json`.

## How to read this

- **Source** uses `script:line` relative to `scripts/` of the bash orchestrator. Line numbers are from its frozen
  version and are approximate where a range is given.
- **CONCEPT §x** and **ADDENDUM §x** point to the original design drafts; their decisions are summarised in
  [`concept.md`](concept.md).
- **Test** gives `tests/<file>#<case>` when a numbered case covers the scenario today, `tests/<file>` (unnumbered
  block) when the file covers it without a number, and `—` when nothing does. fanin needs a test for every
  scenario, including the `—` ones.
- **Mark**:
  - `PARITY`: fanin must behave the same, as seen from the owner and the forge.
  - `CHANGES`: an owner decision of 2026-11-24 changes the behaviour. The line after it says what replaces it.
    Today's test is then a reference, not a target.
- Words used throughout:
  - **Worker statuses**: `DONE PR #n`, `PUSHED <branch>`, `COMMITTED <branch>`, `ISSUE #n`, `NOTHING: …`,
    `BLOCKED: …`.
  - **Loop results**: `idle`, `done`, `ready #n`, `blocked <agent>`, `paused #n`, `stopped`, `ERROR`.
  - **Gate verdicts**: `GREEN`, `PENDING`, `RED`, plus Gitea's `GREEN docs`, `GREEN code` and `NOCI`.

### Changes that run through many scenarios

These are stated once here; the scenarios point back to them.

| Tag | Today | fanin |
|---|---|---|
| C-REPORT | The worker's status is scraped from the pane (`await.sh`, `lib.sh status_line`) | The worker reports through the MCP tool `fanin_report`; `result.json` is the fallback. Pane reading stays only for dialog and stall detection |
| C-STORE | `state/<label>.md` is an append-only log, plus lock and hold files | SQLite is the source of truth; `state/*.md` is rendered from it |
| C-SLOTS | Named lanes `a`–`z`: the `lanes:` header, agent `<p>-<l>-<n>`, branch `lane-<l>-` | Anonymous slots per project/host. No lane names in agent or branch names, no per-lane lock or stop file |
| C-LOOP | The owner session is the event loop (`loop-all.sh`, `watch-agents.sh`, Stop hook) | fanin is the event loop from step 3 on. The owner session only answers through `fanin` MCP/CLI/UI |
| C-CONFIG | State header keys plus `projects.conf` | YAML + JSON Schema (`config/fanin.yaml`, `config/projects/*.yaml`, `worker-kinds.yaml`) |
| C-BUDGET | Per loop: 2 fix nudges, 3 review nudges, 3 stalls | Per run: `fix_rounds_per_run 2`, `rollover_pct 75`, `max_tokens_per_run 3M`. Per task: `max_runs_per_task 3`. A full budget gives a fresh run with a handover (ADDENDUM §2.10) |
| C-LOCK | `flock` on fds 8/9 and a watchdog that kills hung loops by PID | Store rows (`project_locks`, `timers`) with leases; there are no loop processes to kill |

---

## 1. Pick

These scenarios cover how the next task is chosen and with which model.

**PK-1**
- Given: a GitHub project with open todos and no task named by the owner.
- When: the loop needs a task.
- Then: the picker runs headless as haiku, read-only (`gh pr list`/`gh issue list` allowed), in the repo, with a
  600 s timeout. Its last `PICK sonnet|opus: <task>` line sets the task and model.
- Source: `pick.sh`, `loop.sh:433-440`. Test: `pick.sh#1`. Mark: PARITY.

**PK-2**
- Given: a Gitea project.
- When: the picker runs.
- Then: it gets no gh tools, uses its own in-progress rule and uses the project's base branch.
- Source: `pick.sh`. Test: `pick.sh#2`. Mark: PARITY.

**PK-3**
- Given: the picker answers `NOTHING: …`.
- Then: the loop logs it and ends `idle`. No worker is started.
- Source: `loop.sh:436`. Test: `pick.sh#3`. Mark: PARITY.

**PK-4**
- Given: the picker prints junk, an unknown model, a PICK inside a line, or nothing at all (a timeout).
- Then: the picker prints `ERROR` with the output's tail, never a pick. The loop logs "pick failed" and spawns a
  worker that chooses its own task.
- Source: `pick.sh`, `loop.sh:441`. Test: `pick.sh#4,#5`. Mark: PARITY.

**PK-5**
- Given: an owner model, a `model:` header and a picked model.
- Then: the worker model is the owner model, else the header model, else the picked one. Opencode workers get no
  model (the sandbox config fixes it).
- Source: `loop.sh:444-447`. Test: `loop.sh#5`. Mark: CHANGES (C-CONFIG).
  The precedence stays; the header becomes project YAML.

**PK-6**
- Given: an owner task (`--task`) or an owner question (`--analyze`).
- Then: no picker runs. The task is tagged as an owner task and the loop ends after its first merge with `done`
  (it does not take the next todo).
- Source: `loop.sh:405,422-432,269-275`. Test: `loop.sh#5`, `chain.sh#1`. Mark: PARITY.

**PK-7**
- Given: `--task` together with `--analyze`, `--worker opencode` without `--task`, or an unknown model.
- Then: usage error before any spawn.
- Source: `loop.sh:37-79`. Test: `loop.sh#9`. Mark: PARITY (CLI validation).

**PK-8**
- Given: a pick that cost tokens, whether it ends in a pick or `NOTHING`.
- Then: the task gets a `token_usage` event `pick: N tokens (…)` with counts only. A pick without usage (a failed
  one) records no event. The picker model stays the kind's choice (`haiku` is a valid `picker.model`).
- Source: `loop.sh`. Test: `PK-8`. Mark: CHANGES (C-BUDGET): the bash loop did not count the picker.

## 2. Spawn

These scenarios cover starting a worker and handing it its prompt.

**SP-1**
- Given: a task and a free agent name.
- When: spawning.
- Then:
  - The project's herdr workspace is found or created, and a new tab is created.
  - Claude starts with the worker MCP config and the session name `<label> · <short title> · <agent>`.
  - The prompt is sent and the worker is seen working.
- Source: `spawn.sh:73-143`. Test: `spawn.sh#1`. Mark: PARITY.

**SP-2**
- Given: the start fails without an agent appearing.
- Then: retry up to 8× at 120 s. If every try fails, the last herdr error is in the message and the tab stays.
- Source: `spawn.sh:104-122`. Test: `spawn.sh#3,#4`. Mark: PARITY.

**SP-3**
- Given: herdr errors after the agent was created, or an older agent of that name lives elsewhere.
- Then: no second start and no prompt to the foreign agent.
- Source: `spawn.sh:104-122`. Test: `spawn.sh#2,#2b`. Mark: PARITY.

**SP-4**
- Given: Claude shows the folder trust dialog at start.
- Then: the spawn dies at once, naming the pane for the owner.
- Source: `spawn.sh:~118`. Test: `spawn.sh#5`. Mark: PARITY.

**SP-5**
- Given: herdr refuses the prompt, or answers oddly before the start.
- Then: the spawn fails with the reason and the pane name, and never reports a started worker.
- Source: `spawn.sh:133-143`. Test: `spawn.sh#6,#7`. Mark: PARITY.

**SP-6**
- Given: the prompt is not taken by the input box.
- Then: check 10× at 3 s, resend on an empty box, else press Enter.
- Source: `spawn.sh:133-143`. Test: `spawn.sh#1`. Mark: PARITY (adapter detail).

**SP-7**
- The prompt is `worker[-oc][-gitea][-analyze].md`, with `{{INCLUDE:part}}` and `{{REPO}}/{{BASE}}/{{HINT}}`
  filled in.
- A hint is inserted literally. A missing part fails rather than leaving the include in.
- The status-line sentence appears once, right before the format line. The worker reviews its diff before the
  first push and runs no PR-level `/pr-review` (fanind's `fanin/review` is the gate review).
- Source: `lib.sh render_prompt`, `prompts/`. Test: `prompts.sh#1-#4`. Mark: CHANGES (C-REPORT).
  Templates move to `internal/prompt`; the status-line part becomes "call `fanin_report`".

**SP-8**
- Given: a spawn fails.
- Then: the loop logs `spawn failed <agent>`, ends `ERROR`, and the next spawn skips that number.
- Source: `loop.sh:450-455`, `next_agent`. Test: `loop.sh#38`. Mark: CHANGES (C-STORE).
  A failed run is a run row; numbering comes from the store.

**SP-9**
- Given: an opencode worker.
- Then:
  - GitHub projects only. Gitea projects, analysis questions and unknown kinds are refused.
  - The sandbox clone is synced to `origin/main`; a dirty clone stops the spawn before any herdr call ("is not
    clean").
  - The shim is put on PATH and the oc prompt is sent.
- Source: `spawn.sh:58-71`. Test: `spawn-oc.sh#1-#3`. Mark: PARITY.

**SP-10**
- Given: the state names a live worker of this project/lane and no `--agent` was given.
- Then: that worker is resumed, not a second one started. A worker that chain.sh keeps open for the owner is
  skipped, and a fresh one starts beside it.
- Source: `loop.sh:146-156,398-410`. Test: `loop.sh#36,#36c`. Mark: CHANGES (C-STORE).
  The live run comes from the store, not from herdr plus log parsing.

**SP-11**
- Given: herdr cannot say whether the worker lives.
- Then: `ERROR: status of <agent> unknown …, no second worker started`.
- Source: `loop.sh:398`. Test: `loop.sh#36`. Mark: PARITY.

## 3. Await, stall, nudge budgets

These scenarios cover waiting for a worker's result and nudging it when it stops.

**AW-1**
- Given: a worker prints a status line in its reply.
- Then:
  - The line is taken only when printed after the last nudge marker.
  - Not as a status: lines inside the echoed prompt, a nudge text sitting in the input box, and a line cut at
    the screen edge.
  - A wrapped line is joined first.
- Source: `lib.sh status_line`, `await.sh:116-126`. Test: `await.sh#6,#10,#11,#22`, plus the unnumbered
  cut/wrap blocks. Mark: CHANGES (C-REPORT).
  `fanin_report` replaces the scraping; a report counts only when it comes from the current run's token.

**AW-2**
- Given: a long BLOCKED question.
- Then: it stays one line, cut to 400 chars.
- Source: `await.sh:116-126`. Test: `await.sh` (unnumbered "long BLOCKED"). Mark: PARITY (field caps, ADDENDUM
  §2.10.1-5).

**AW-3**
- Given: a background agent, subagent or shell footer is live.
- Then: keep waiting (up to 50 waits, `MAX_BG_WAITS`) and pick up the later status. A stale footer far up the
  scrollback does not count.
- Source: `await.sh:149-167`. Test: `await.sh#1-#5,#7`. Mark: PARITY.

**AW-4**
- Given: a pane unchanged for 3 slices of 300 s (only timers or a spinner change).
- Then: `NO-STATUS stalled:quiet`. Real output resets the count.
- Source: `await.sh:95-114`. Test: `await.sh#13-#16`. Mark: CHANGES (C-REPORT).
  A stall is a missing `fanin_progress` heartbeat for `stall_after`; the pane fingerprint stays as a fallback for
  workers without MCP.

**AW-5**
- Given: a background shell whose tail stays the same for one 20 min window.
- Then: `stalled:bg-shell`. A change restarts the window.
- Source: `await.sh:149-167`. Test: `await.sh#17-#21`. Mark: CHANGES (C-REPORT), same as AW-4.

**AW-6**
- Given: a herdr read fails.
- Then: retry 3× at 30 s, then `herdr unreachable`. The loop ends `ERROR` and never nudges or blocks the worker. A
  hiccup between two samples is re-read and not taken as "settled".
- Source: `await.sh:48-64`, `loop.sh:561`. Test: `await.sh#25`, `loop.sh#39`. Mark: PARITY.

**AW-7**
- Given: an idle worker whose pane reads empty once.
- Then: read again, never NO-STATUS. A gone agent has no retry.
- Source: `await.sh:48-64`. Test: `await.sh` (unnumbered). Mark: PARITY.

**AW-8**
- Given: `--resume` with a nudge left unsent in the input box.
- Then: submit it with Enter (at most 2×) and wait at most `RESUME_GRACE_S=120` for "working".
- Source: `await.sh:73-91`. Test: `await.sh#8,#9,#12`. Mark: PARITY (adapter detail).

**AW-9**
- Given: an approval dialog or an AskUserQuestion dialog.
- Then:
  - `blocked NO-STATUS dialog: <question + options>`, and the loop ends `blocked <agent>: dialog: …`.
  - It says "unreadable" when no text can be read.
  - A numbered list in a normal reply is no dialog.
- Source: `await.sh:116-126`, `loop.sh:565-571`. Test: `await.sh#26`, `loop.sh#41b`. Mark: CHANGES (C-LOOP).
  It becomes an `owner_questions` row (`needs_owner`), shown in the UI inbox, and one note to the owner. The same
  dialog again asks nothing new; a changed dialog supersedes the open question. An unreadable dialog only notes the
  owner (answer it in the pane).

**AW-10**
- Given: NO-STATUS with a stall mark.
- Then: one stall nudge. A second stall gives `blocked …: no progress for 20 min after the stall nudge; tail: …`.
- Source: `loop.sh:572-580`. Test: `loop.sh#29`. Mark: CHANGES (C-BUDGET).
  The second stall may roll over to a fresh run while `runs < max_runs_per_task`, else `needs_owner`.

**AW-11**
- Given: NO-STATUS without dialog or stall.
- Then, in order:
  - A live bg footer: wait again (at most 3×).
  - Else one `NUDGE_CONTINUE`.
  - Else one `NUDGE_SECURE` if there are unpushed commits.
  - Else `blocked …: no status; unpushed: …; tail: …`.
- Source: `loop.sh:581-600`. Test: `loop.sh#16,#16a` and "two silent stops block". Mark: CHANGES (C-REPORT).
  A run that ends without `fanin_report` or `result.json` follows the same ladder.

**AW-12**
- Given: a silent stop.
- Then: the worker's last words are logged (input box and footer dropped) and the full tail goes to
  `state/<agent>.tail`. The unpushed scan covers only this lane's branches.
- Source: `loop.sh last_said, unpushed_work`. Test: `loop.sh#16a,#16`. Mark: CHANGES (C-STORE, C-SLOTS).
  The tail is stored per run; the scan is per run branch.

**AW-13**
- Given: any nudge that herdr cannot deliver.
- Then: the loop ends `ERROR: nudge to <agent> failed` at once, and nothing is awaited after it.
- Source: `lib.sh nudge`, `loop.sh prompt_worker`. Test: `loop.sh#30`. Mark: PARITY.

**AW-14**
- Given: a Claude worker's status line updates.
- Then: `fanin worker-usage` posts the context tokens and the context share to `POST /api/v1/runs/{id}/usage`.
  fanind adds each new response's tokens to the run's total and stores the share. A repeated update counts
  nothing. A wrong token or an older run records nothing. `max_tokens_per_run` and `rollover_pct` then hand the
  run over (C-BUDGET).
- Source: —. Test: `internal/model/usage_test.go`, `internal/report/report_test.go`,
  `internal/api/report_routes_test.go`, `pkg/faninmcp/statusline_test.go`, `pkg/faninmcp/mcpconfig_test.go`,
  `cmd/fanin/cmd_workerusage_test.go`,
  `internal/sched/resume_test.go`, `internal/runtime/herdr/start_test.go`. Mark: CHANGES (C-BUDGET).

## 4. GitHub gate

These scenarios cover `pr-gate.sh` and the waits around it.

**GH-1**
- Given: the PR is open and mergeable, checks passed (skipped is fine), and the last "PR quality review" comment
  has a "Merge readiness" verdict of ready, updated after the head commit.
- Then: `GREEN <title>`.
- Source: `pr-gate.sh:30-60`. Test: `pr-gate.sh#1,#4`. Mark: PARITY.

**GH-2**
- Then RED for each of these:
  - The review is older than the head commit.
  - The verdict is "not ready" below the heading.
  - There is no verdict, or no review at all.
  - Only the last review comment counts.
- Source: `pr-gate.sh`. Test: `pr-gate.sh#2,#3,#5,#6`. Mark: PARITY.

**GH-3**
- Then PENDING for each of these:
  - Checks running (review findings wait until CI settles).
  - No checks yet.
  - `mergeable=UNKNOWN`.
- Source: `pr-gate.sh`. Test: `pr-gate.sh#7-#9`. Mark: PARITY.

**GH-4**
- Then RED for each of these:
  - A failed or cancelled check, ahead of running ones, with the review reasons appended.
  - `CONFLICTING`.
  - The PR is not open.
- Source: `pr-gate.sh`. Test: `pr-gate.sh#10,#11`. Mark: PARITY.

**GH-5**
- Given: the PR, the review edit time or the PR JSON cannot be read.
- Then: an error and a non-zero exit, never a verdict.
- Source: `pr-gate.sh`. Test: `pr-gate.sh#12-#14`. Mark: PARITY.

**GH-6**
- Given: the gate itself fails or prints nothing.
- Then: retry 3× at 60 s, then `ERROR: gate unreachable`. Nobody is nudged.
- Source: `loop.sh:193-203`. Test: `loop.sh#34`. Mark: PARITY.

**GH-7**
- Given: PENDING.
- Then: wait with `gh pr checks --watch` (at most 1800 s, 20 s settle), up to 12 waits, then
  `RED: still … after 12 waits`. Network failures of the watch do not use up a wait (at most 6).
- Source: `loop.sh:206-219`. Test: `loop.sh#1,#35`. Mark: CHANGES.
  fanin waits on webhooks or polls through `timers`; the limits become config.

**GH-8**
- Given: DONE, and the worker ended right after its review.
- Then: the loop waits for CI itself and merges without a nudge.
- Source: `loop.sh:467-480`. Test: `loop.sh#1`. Mark: PARITY.

**GH-9**
- Given: RED from CI.
- Then: a fix nudge with the reason and the redacted tail (at most 4 KB) of the failed step's log, up to 2, then
  `paused #n: … (worker <a> still open)`. A log that cannot be read is warned about; the nudge goes with the reason alone.
- Source: `loop.sh:237-266`. Test: `loop.sh#3`. Mark: CHANGES (C-BUDGET).
  After 2 fix rounds in a run, a fresh run starts with a handover; after 3 runs, `paused`.

**GH-10**
- Given: a review-only RED.
- Then: the review nudge carries the review comment's text (≤ 1500 chars; the gate's reasons if it is unreadable), does not use the fix budget, up to 3, then `paused`.
- Source: `loop.sh:237-266`. Test: `loop.sh#2,#4`. Mark: CHANGES (C-BUDGET), same as GH-9.

**GH-11**
- Given: an analysis DONE PR that is GREEN but changes more than docs.
- Then: `ready` for the owner's go, no merge.
- Source: `loop.sh:472-476`. Test: `docs-only.sh`. Mark: PARITY.

**GH-12**
- The docs-only rule: the file list is non-empty and every path matches `^docs/|\.md$`, except any `CLAUDE.md`
  and anything under `.claude/` (that counts as code).
- Source: `lib.sh:26-34`. Test: `docs-only.sh`. Mark: PARITY.

**GH-13**
- Given: a GitHub DONE PR that is GREEN.
- Then: it merges without the owner (merge grant).
- Source: `loop.sh:477`, `SKILL.md` ground rules. Test: `loop.sh#1`. Mark: PARITY (owner decision: GitHub
  auto-merges on green).

**GH-14**
- Given: a DONE line without a PR number, a PR that another lane's worker claims, or a PR that heads the base
  branch.
- Then: `blocked` with the line, never gated or merged.
- Source: `loop.sh:378-395`. Test: `loop.sh#37,#41`. Mark: CHANGES (C-REPORT).
  `fanin_report` carries the PR number; fanin checks that the PR's head is the run's branch.

## 5. Gitea gate + dry-activate (NixOS config repo)

These scenarios cover `gitea-gate.sh`, `gitea-pr.sh` and the dry-activate step.

**GT-1**
- Given: a `PUSHED <branch>` on a Gitea project.
- Then:
  - Odd or base branches are refused; an open PR for the branch is reused.
  - Otherwise a PR is opened against the base: titled by the oldest commit, the body lists every commit, plus
    the "Opened by the orchestrator" footer.
  - If the open PRs cannot be listed: an error, never a second PR.
- Source: `gitea-pr.sh`. Test: `gitea-pr.sh#1-#6`. Mark: PARITY (fanin's footer reads "Opened by fanin.").

**GT-2**
- Given: PUSHED on a GitHub project.
- Then: `ERROR … is not a gitea project`.
- Source: `loop.sh:506`. Test: `gitea-pr.sh#6`. Mark: PARITY.

**GT-3**
- The verdicts:
  - Open, mergeable, checks green, docs-only: `GREEN docs`.
  - Any non-docs file (CLAUDE.md included): `GREEN code`.
- Source: `gitea-gate.sh`. Test: `gitea-gate.sh#1,#2`. Mark: PARITY.

**GT-4**
- No status yet: PENDING "no checks", which becomes `NOCI` after 5 polls at 60 s.
- Running checks: polled until settled, RED after 40 polls.
- Source: `loop.sh:223-234`. Test: `gitea-gate.sh#3,#4`, `loop.sh#31-#33`. Mark: PARITY (limits become config).

**GT-5**
- Then RED for each of these, all reasons together:
  - A failed check.
  - A conflict (it wins over running checks).
  - The PR is closed or merged, or aimed at another base.
- Source: `gitea-gate.sh`. Test: `gitea-gate.sh#5-#7`. Mark: PARITY.

**GT-6**
- Given: an MCP failure.
- Then:
  - PR unreadable: the gate dies.
  - Status or files unreadable: PENDING, never GREEN.
  - A non-numeric PR dies before any call.
- Source: `gitea-gate.sh`. Test: `gitea-gate.sh#8,#9`. Mark: PARITY.

**GT-7**
- Given: `GREEN docs`.
- Then: auto-merge under the merge lock and post-merge hook, as `(docs-only, auto)`.
- Source: `loop.sh:513-516`. Test: `loop.sh#25`. Mark: PARITY (owner decision).

**GT-8**
- Given: `GREEN code` or `NOCI`.
- Then:
  - `dry-activate.sh` fetches the base and the branch, prints the firewall-diff line, and runs
    `nixos-rebuild dry-activate` in a throwaway worktree.
  - The loop ends `ready #n (ci; dry-activate: firewall: …; would: …)`. The owner's go merges it.
- Source: `loop.sh:520-524`, `dry-activate.sh`. Test: `loop.sh#32` (NOCI path), `firewall-diff.sh`; dry-activate
  itself: —. Mark: PARITY.
- fanin: generic `pre_go_check` (`hook.PreGo`), tests `TestPreGo/GT-8_*` in `internal/sched` and `internal/hook`.
  A failing or missing check asks the owner and never merges (FP-6). The NixOS `dry-activate` stays outside the repo.

**GT-9**
- `firewall-diff`: changes to firewall, openFirewall, iptables/nft, compose ports or `network_mode: host` are
  summarised; otherwise "no port or firewall rule changes".
- Source: `firewall-diff.sh`. Test: `firewall-diff.sh`. Mark: PARITY.
- fanin: not ported; the script stays outside the repo and runs as a project's `pre_go_check`.

**GT-10**
- Gitea calls go through the MCP (`python3 -I gitea_mcp.py`); the token stays in gitea-mcp.
- Source: `lib.sh gitea()`. Test: `gitea-gate.sh`. Mark: CHANGES.
  The Go `forge/gitea` client must keep the token out of fanin's logs and config dumps.

## 6. Conflicts

These scenarios cover a PR that conflicts with main.

**CF-1**
- Given: the gate says `CONFLICTING`.
- Then: one conflict nudge ("merge origin/main"), taken from the CI budget, then a re-gate and a merge.
- Source: `loop.sh:105,237-266`. Test: `loop.sh#26`. Mark: PARITY.

**CF-2**
- Given: the PR still conflicts after that nudge.
- Then: `paused`, even if the CI budget has a nudge left.
- Source: `loop.sh:237-266`. Test: `loop.sh#27`. Mark: PARITY.

**CF-3**
- Given: finish.sh finds the PR conflicting (main moved after the gate).
- Then: it is treated as `RED mergeable=CONFLICTING`, with the conflict nudge, and then merged.
- Source: `loop.sh:347-350`. Test: `loop.sh#28`, `finish.sh#4`. Mark: PARITY.

**CF-4**
- The worker merges `origin/main` into its branch; it never rebases or force-pushes. A rewritten opencode branch
  is refused ("it is never forced").
- Source: `prompts/`, `oc-pr.sh`. Test: `oc-pr.sh#3`. Mark: PARITY.

## 7. Merge lock, post-merge hook, merge hold

These scenarios cover how merges are serialised and what happens after one.

**MG-1**
- One merge (re-gate, finish, hook) at a time per project, across lanes.
- With no contention, the hook runs once in `STATE_DIR` with `ORCH_PROJECT`/`PR`/`LANE`; there is no re-gate and
  no wait.
- Source: `loop.sh:285-316`. Test: `loop.sh#17`. Mark: CHANGES (C-LOCK).
  The lock is a `project_locks` row with holder and lease; the hook gets the same environment.

**MG-2**
- Given: the lock is taken.
- Then: wait up to 3600 s, re-gate after the other merge's hook ended, then merge. A red re-gate gives a nudge
  instead, and the merge happens on the next green. A timeout gives `ERROR: merge lock … held for over …s by …`.
- Source: `loop.sh:293-301,340-345,359`. Test: `loop.sh#19,#23,#24`. Mark: CHANGES (C-LOCK).

**MG-3**
- Given: the lock holder dies in finish.sh.
- Then: the lock frees and the next merge proceeds.
- Source: `flock` semantics. Test: `loop.sh#21`. Mark: CHANGES (C-LOCK).
  The lease expires, or the holding run is known dead.

**MG-4**
- Given: the hook fails.
- Then:
  - The PR counts as merged, and the loop ends `ERROR … merging held for all lanes until rm <hold>`.
  - The hold says `failed: <last line>`.
  - Every other merge pauses with `paused #n: merge held (…) (worker still open)`.
- Source: `loop.sh:346-358`, `merge_serial`. Test: `loop.sh#18,#20`. Mark: CHANGES (C-STORE).
  The hold is a `holds` row, lifted by `fanin hold lift` or the UI.

**MG-5**
- Given: the holder dies inside its hook.
- Then: the deploy state is unknown, the hold stays, and the others pause.
- Source: `merge_serial` (the hold is written before the hook). Test: `loop.sh#22`. Mark: PARITY (write the hold
  before the hook runs).

**MG-6**
- finish.sh:
  - Squash merge with subject `<title> (#n)` and a body of trailers only.
  - On GitHub, network failures are retried at 10/30/60 s, and the PR is checked for MERGED before a retry merges
    again.
  - Gitea uses `delete_branch`.
  - A view without a head branch dies before the merge.
- Source: `finish.sh`. Test: `finish.sh#1-#5`. Mark: PARITY.

**MG-7**
- After a merge: delete the remote branch, worktree and local branch, and prune.
  - A clean checkout on the base is fast-forwarded.
  - A dirty worktree, or a checkout off base or diverged, stays as it is, with a warning.
- Then close the pane (never the tab) and restore the tab title.
- Source: `finish.sh`, `remote/cleanup-branch.sh`. Test: `remote.sh#1-#5`. Mark: PARITY.

**MG-8**
- CI hold:
  - While `state/<p>.ci-hold` stands: no fresh worker ("paused: CI held, no new worker") and no merge (CI-HELD),
    except by the named ci-fix worker.
  - The fix's merge lifts the hold and nudges the other workers with `NUDGE_CI_FIXED`.
  - A failed nudge is named in the lift line.
  - `--ci-fix` without a hold merges like any task.
- Source: `loop.sh:298,319-329,415-418,457`. Test: `loop.sh#42`, `hc-chain.sh#6-#8c`, `hc-lane.sh#13,#14`.
  Mark: CHANGES (C-STORE).
  The CI hold becomes a `holds` row of kind `ci` with `fix_task`.

## 8. Lanes

These scenarios cover today's named lanes, which slots replace.

**LN-1**
- The default lane `a` keeps today's names. Lane `b` uses agents `<p>-b-<n>`, tags its state lines, has the loop
  line `repo/b:`, and gets the branch hint `lane-b-`.
- Source: `lib.sh`, `loop.sh`. Test: `lanes.sh#1,#2`, `loop.sh#11`. Mark: CHANGES (C-SLOTS).
  There are no lane names; a task gets a free slot and its branch is its own.

**LN-2**
- `--lane` without a `lanes:` header, for an unlisted lane, or with a malformed name: refused before any spawn.
- Source: `loop.sh:37-79`. Test: `loop.sh#10`, `chain.sh#8`. Mark: CHANGES (C-SLOTS).
  Becomes `max_parallel` per project.

**LN-3**
- Each lane has its own lock and stop file, and ignores the other lane's live worker.
- Source: `loop.sh:88-89,414`. Test: `loop.sh#12-#14`. Mark: CHANGES (C-SLOTS).
  Pausing is per task or per project.

**LN-4**
- Lanes take only owner tasks with Claude workers: the picker, `--analyze` and opencode are refused.
- Source: `loop.sh:420-421`. Test: `loop.sh#15`. Mark: CHANGES (C-SLOTS).
  Any task kind may take a slot.

**LN-5**
- `open_readies`, `lane_events` and the lane branch filter are scoped to a lane. A merge logged by any lane closes
  a ready.
- Source: `lib.sh`. Test: `lanes.sh#3-#6`, `lib-helpers.sh#2`. Mark: CHANGES (C-STORE).
  These are queries by `task_id`.

**LN-6**
- Parity that survives slots: two tasks of one project never merge at once (MG-1), and a project-wide hold stops
  every slot (MG-4, MG-8).
- Test: `loop.sh#18-#22`. Mark: PARITY.

## 9. Chain

These scenarios cover `chain.sh`, which runs a file of tasks in order.

**CH-1**
- One task per line (`model|task`), run as `loop.sh --task` in order with the line's model.
- `#` and blank lines are skipped but still counted.
- The file is re-read before every task.
- Source: `chain.sh`. Test: `chain.sh#1,#4`. Mark: CHANGES.
  A chain becomes queued tasks with `after:` dependencies in the store; edits to the file are re-imported.

**CH-2**
- Given: a non-done task.
- Then: the chain stops and prints a resume command (`--start n --resume-first`). Nothing after it runs.
- Source: `chain.sh`. Test: `chain.sh#2,#3`. Mark: CHANGES.
  Dependent tasks stay queued; the rest of the queue runs.

**CH-3**
- The `.chain.stop` file ends the chain before the next task and is consumed.
- Usage errors and a missing file die before any loop runs.
- Source: `chain.sh`. Test: `chain.sh#5,#6`. Mark: CHANGES (`fanin pause`).

**CH-4**
- `--lane b`: every loop call carries the lane, and the log lines are tagged.
- Source: `chain.sh`. Test: `chain.sh#7`. Mark: CHANGES (C-SLOTS).

**CH-5**
- The hc-chain templates (`docs/dev/lanes-hc-chain/`) add:
  - Cross-lane `dep`s.
  - Parking of blocked tasks.
  - One fresh opus finisher for a paused PR.
  - CI-red-outside-diff, which sets a CI hold and sends one Signal note.
  - `lane.sh` waits on holds and stop files and restarts on a changed tasks file.
  - A lint of the tasks file.
- Source: `docs/dev/lanes-hc-chain/`. Test: `hc-chain.sh#1-#12`, `hc-lane.sh#1-#14`, `lint-tasks.sh#1-#4`.
  Mark: CHANGES.
  These are the reference for fanin's dependency, park and retry rules (`max_runs_per_task`), and its
  CI-hold-on-foreign-red rule.

## 10. watch-agents, arm/park/check, Stop hook

These scenarios cover how the owner session learns that a worker settled.

**WA-1**
- The watcher watches every named herdr agent on the control host and the worker host that no running loop covers.
  - A loop covers only its own lane's workers; the opencode loop covers only `oc-` workers.
  - Once a loop is gone, or a watchdog mark exists, its worker is watched.
- Source: `watch-agents.sh loop_covers`. Test: `watch-agents.sh` (unnumbered "loop covers"/"watchdog's marker").
  Mark: CHANGES (C-LOOP).
  fanin supervises every run it started; agents it did not start are owner sessions and not its business.

**WA-2**
- Given: one agent settles while another hangs.
- Then: the watcher reports the settled one (`== a (m): <line>`) and parks it. The hung wait is killed.
- Source: `watch-agents.sh`. Test: `watch-agents.sh` (unnumbered). Mark: CHANGES (C-LOOP): `fanin wait --owner`
  returns one event.

**WA-3**
- `arm <agent> "<answer start>"` expects a new status after an answer.
- `park <agent> <why>` keeps an agent watched for a pane answer: it reports `answered-in-pane`, or the pane
  vanishing, as a fresh report.
  - An unchanged parked agent only shows as `still-parked` after 3 waits.
  - A re-park keeps the line.
- Source: `watch-agents.sh`, `await.sh:130-144`. Test: `watch-agents.sh`, `await.sh#27`. Mark: CHANGES (C-LOOP).
  An answer typed in the pane is detected by the adapter, and the question row is closed `answered in the pane`;
  the task goes back to running. A key fanin pressed is no pane answer.

**WA-4**
- The Stop hook (`watch-agents.sh check`) refuses to end the owner's reply (exit 2) while an agent is unwatched or
  the watcher is missing.
  - A list failure blocks once per failure.
  - It is skipped on the dev machine, and in a checkout without the default state dir.
- Source: `.claude/settings.json`, `watch-agents.sh check`. Test: `watch-agents.sh` (unnumbered). Mark: CHANGES
  (C-LOOP). Retired after step 3.

**WA-5**
- Given: a machine that herdr cannot list.
- Then: the watcher says so and exits 1, never "nothing to watch". Dead entries are pruned only when every machine
  answered.
- Source: `watch-agents.sh`. Test: `watch-agents.sh` (unnumbered). Mark: PARITY (host down ≠ agents gone).

**WA-6**
- A watcher still listing holds its lock without having written its set. The lock is not inherited by the waits.
- Source: `watch-agents.sh`. Test: `watch-agents.sh`. Mark: CHANGES (C-LOCK).

## 11. Watchdog

These scenarios cover how a hung loop is detected and killed.

**WD-1**
- A loop is hung when it holds its lock, its newest event (or gate beat) is ≥ 45 min old, and the worker is
  idle, done or gone.
- Then: kill the loop's process subtree (found through the lock's `/proc` fd openers), write the `.watchdog`
  mark, and log `watchdog: … hung`.
- Source: `watchdog.sh`. Test: `watchdog.sh#2,#5,#8`. Mark: CHANGES (C-LOCK).
  fanin has no loop processes. A run without a heartbeat or result for `worker_timeout` is ended by the timer.
  fanin itself is supervised by systemd.

**WD-2**
- Spared:
  - No lock or a dead holder.
  - A working or blocked worker.
  - A merge lock held by the loop.
  - A worker's own bg footer, up to 180 min.
- Source: `watchdog.sh`. Test: `watchdog.sh#1,#3,#4,#10`. Mark: PARITY (as run-timeout exceptions).

**WD-3**
- Given: an unreadable worker status (no jq) or a corrupt event date.
- Then: the watchdog dies loudly or reports the lane unreadable. It never kills.
- Source: `watchdog.sh`. Test: `watchdog.sh#6,#6b,#9`. Mark: PARITY (fail closed: an unknown state never ends a
  run).

**WD-4**
- It works when started by systemd with a bare environment.
- Test: `watchdog.sh#7`. Mark: PARITY (fanin's unit, `deploy/systemd/fanind.service`).

**WD-5**
- Given: a run that is still working `worker_max_wall` (90 min) after its start.
- Then: it is handed over to a fresh run with `wall time spent: …`, or its task pauses after `max_runs_per_task`
  runs. A dialog, a held lock, an own background shell or an unknown status still spares it.
- Source: none; the bash watchdog has no wall limit. Mark: CHANGES (C-BUDGET).

## 12. Opencode path + review

These scenarios cover sandboxed opencode workers and the PR review.

**OC-1**
- Given: `COMMITTED oc/<branch>` from an opencode worker.
- Then:
  - `oc-pr.sh` fetches from the sandbox remote and pushes fast-forward only (no force).
  - It opens the PR once, with the commit subject, and the body names the reviewer.
  - It reuses the open PR on fix commits.
  - It refuses a non-`oc/` branch, a missing remote, a Gitea project, or a branch the clone lacks.
- Source: `oc-pr.sh`, `loop.sh:481-485`. Test: `oc-pr.sh#1-#6`. Mark: PARITY.

**OC-2**
- `COMMITTED` from a Claude worker is an ERROR.
- Source: `loop.sh:482`. Test: `loop.sh#9`. Mark: PARITY.

**OC-3**
- Claude review:
  - Headless `/pr-review` in a detached worktree of the PR branch.
  - Read-only tools plus `gh pr comment`, 1800 s timeout.
  - The prompt tells the skill to skip its fix step, `make ci` and its CI and branch freshness sections.
  - The comment must exist, and is printed cut to 1500 chars.
  - The worktree is removed either way.
  - fanind starts it once CI is green on a head without `fanin/review`: one run per head, outside the worker
    slots; a failed run posts nothing and runs again on the next gate tick.
  - A head that only merges the base into a head with a ready `fanin/review`, without conflicts or own changes,
    gets `fanin/review` ready without a run; CI still gates it. A head with own changes is reviewed. The third failed run on one head
    goes to the owner instead.
- Source: `review.sh`. Test: `review.sh#1-#4`. Mark: PARITY (ADDENDUM: a headless run with usage accounting).

**OC-4**
- Opencode review:
  - Runs in the sandbox with the cloud-fallback model (or `ORCH_REVIEW_MODEL`/the `review:` header).
  - The diff is capped at 60000 bytes, with a note.
  - The clone must be at the PR head and unchanged after the review, else nothing is posted.
  - Posted with a model footer.
- Source: `review.sh`. Test: `review.sh#5-#7`. Mark: PARITY.

**OC-5**
- A review without the header or verdict is retried once with a correction note.
- Accepted: a preamble, fences and reworded verdicts.
- Failures (no text, no mark, a sudo password prompt naming the sudoers line) give ERROR.
- Source: `review.sh`. Test: `review.sh#8-#10`. Mark: PARITY.

**OC-6**
- A refused push or a failed review stops with ERROR before the gate.
- Source: `loop.sh:484-488`. Test: `loop.sh#8`. Mark: PARITY.

**OC-7**
- Opencode GREEN with `oc-merge: auto`: merged via finish.sh `(opencode, auto)`. Without it: `ready`.
- Source: `loop.sh:493-499`. Test: `loop.sh#9a`. Mark: CHANGES.
  The owner decision "GitHub auto-merges on green" makes auto the default for every GitHub worker kind; the
  per-project switch stays in YAML.

**OC-8**
- Opencode RED: `NUDGE_OC_FIX`. A review RED sends the reviewer's text with it, and the next commit is reviewed
  again. The fix budget is 2.
- Source: `loop.sh:500-505`. Test: `loop.sh#6,#7,#9b`. Mark: CHANGES (C-BUDGET).

## 13. deploy-release

**DR-1**
- Given: a GitHub repo with a release tag.
- Then:
  - Find the tag (`v`, plain or `<repo>-v`; 10 lookups at 30 s).
  - Find the docker-push workflow run and watch it.
  - Read the image digest.
  - Find exactly one compose module in the NixOS config repo's `origin/master`.
  - If already current: "nothing to do". Else run `loop.sh <nixos-project> --task "bump …" --model sonnet`
    (`--print-task` only prints).
- Source: `deploy-release.sh`. Test: `internal/intake/release_test.go`. Mark: CHANGES.
  Becomes an intake source (release webhook, IN-5) that queues a task on the NixOS config repo. The nixos code gate
  (owner's go) applies.

**DR-2**
- No or several compose modules, no workflow, or no digest: ERROR, no task.
- Source: `deploy-release.sh`. Test: `internal/intake/release_test.go`. Mark: PARITY.

## 14. status / overview / agents

These read-only views move to the snapshot API and the UI.

**ST-1**
- `status.sh`: one line per project/lane with `loop=`, `worker=`, `merge=free|held by …|stale`, `last:` and
  `open:` (ready, blocked, paused, needs-owner, loop-all result, merge hold, CI hold with fix worker).
  - A lock held after its loop died is `stale`.
  - A blocked agent that settled with a new line no longer waits.
  - Without arguments it lists the registry projects in order.
- Source: `status.sh`. Test: `status.sh`, `registry.sh#6`. Mark: CHANGES (C-STORE): `fanin status` and
  `/api/v1/snapshot`.

**ST-2**
- `overview.sh` (German):
  - Sections: Erledigt (the current run only, newest 4 plus a count), Weiter (queued and not started), what waits
    on the owner, CI and merge holds with the file to remove, decisions, and lane blocks.
  - `--close-done` closes finished workers' panes.
- Source: `overview.sh`. Test: `overview.sh#1-#8`, `rotate-state.sh#7`, `cmd/fanin/cmd_overview_test.go`. Mark: CHANGES
  (C-STORE): the UI board (ADDENDUM §2.11.3). `fanin overview` prints the board sections of the snapshot as English
  text: done this run (count and newest four), next, waiting for owner, ready for go (PR number and verdict line,
  the snapshot's `ready` section), holds with the `fanin hold lift` command,
  decisions. An unreachable daemon is an error, never an empty board.

**ST-3**
- `agents.sh` (German): one line per agent, waiting-on-owner first, managed vs owner sessions. One machine down
  gives one warning line, and the other machine is still listed.
- Source: `agents.sh`. Test: `agents.sh`. Mark: CHANGES (Hosts & Worker view).

**ST-4**
- The project registry:
  - `projects.conf` gives path, machine, forge, base and fallback.
  - State header lines win over it.
  - `machine: local` means this host.
  - An unknown machine dies.
- Source: `lib.sh load_project`. Test: `registry.sh#1-#7`, `lib-header.sh#1-#6`. Mark: CHANGES (C-CONFIG).
  JSON Schema validation at load; a bad config refuses to start.

## 15. rotate-state

**RS-1**
- Run manually at ≥ 100 KiB (or with `--force`), keeping 300 records. It keeps:
  - The header and open readies.
  - The newest blocked/paused entry (per lane).
  - Open queued lines and owner decisions of the last 7 days.
  - The highest spawn per prefix and the current run.
- The rest is archived to `state/archive/<p>-<date>.md`. Kept plus archived equals before.
- Source: `rotate-state.sh`. Test: `rotate-state.sh#1-#3b,#6`. Mark: CHANGES (C-STORE).
  The rendered Markdown shows a window. The store keeps everything, with retention by policy.

**RS-2**
- Appends during the swap survive. A failure leaves the file byte for byte as it was.
- Test: `rotate-state.sh#4,#5`. Mark: CHANGES.
  Rendering writes a temp file and renames it; it never edits in place.

## 16. Owner answers, dialogs, loop-all `next:`

These scenarios cover how owner answers reach workers and what the owner session runs next.

**OA-1**
- `answer.sh` on a blocked dialog: only an option number 1-9 is pressed as a key. Free text is refused, with the
  dialog's options, and nothing is sent or recorded.
- Source: `answer.sh`. Test: `answer.sh`. Mark: CHANGES (C-LOOP): `POST /tasks/{id}/answer` validates the option,
  and fanind presses it as a key in the run's pane (`herdr agent send-keys`).

**OA-2**
- A question in the transcript (the worker is idle): the answer goes in as a prompt, and
  `owner answer for <agent>: …` is logged.
- Source: `answer.sh`. Test: `answer.sh`. Mark: PARITY (as an event).

**OA-3**
- Given: a gone agent, an ended run, herdr not answering, or a failed send.
- Then: ERROR, and nothing is recorded; the question stays open.
- Test: `answer.sh`. Mark: PARITY.

**OA-4**
- `loop-all.sh` runs every loop detached, collects `idle`/`done`, and returns at the first attention line with a
  `next:` recipe:
  - dialog or answer for `blocked`.
  - `merge pr=… go: finish.sh` for `ready`, with no `then:` for an owner task.
  - `restart:` for `paused`.
  - `tell-owner`.
- It ends `running: … | continue:` or "all loops ended".
- Source: `loop-all.sh`. Test: `loop-all.sh#1-#6`. Mark: CHANGES (C-LOOP). Retired after step 3.

**OA-5**
- A loop started elsewhere is reported, not watched or restarted.
- A result that arrived while nobody watched is reported first.
- A loop that ended without a line (killed) needs the orchestrator.
- Source: `loop-all.sh`. Test: `loop-all.sh#3,#4,#8`. Mark: CHANGES (C-LOOP).

**OA-6**
- The owner's go on a `ready` PR runs `finish.sh <p> <n> <agent>`.
- Source: `loop.sh ready()`. Test: `loop.sh#5`. Mark: PARITY (`POST /tasks/{id}/go`).

**OA-7**
- Signal is a note to self only (`--to` must be the own number), prefixed `[<host>/orchestrator]`, at most 600
  chars, and logged with `--project`.
- Source: `signal.sh`. Test: `signal.sh`. Mark: PARITY (the push channel links to `/#/task/<id>`).

**OA-8**
- `fanin task cancel <task> [--reason <text>]` (owner token, `POST /api/v1/tasks/{id}/cancel`) ends a stale task
  for good: a queued task leaves the queue; a running, gating or needs_owner task has its open run canceled, its
  slot and merge lock freed and its worker's pane closed, so the next queued task spawns. Nothing is merged and
  nothing is written to the forge; the PR stays as it is. Its open question closes, and one `task_cancelled`
  event with the reason (or `cancelled by the owner`) goes to the log and the board.
- A done, merged (`post_merge`) or already canceled task is refused with 409 and nothing is logged; an unknown
  task is 404, a missing token 401. A go that waits for the lock when the cancel lands drops its merge.
- Source: —. Test: —. Mark: CHANGES (a task merged outside fanind blocked its slot in `gating`).

## 17. Failure paths

These scenarios cover herdr, gh and ssh errors and the scripts' strictness.

**FP-1**
- Loop lock: a second loop for the same project/lane dies "already running".
- A killed loop frees its lock at once; children never inherit fds 8/9.
- Source: `loop.sh:88-89`, `lib.sh drop_locks`. Test: `loop.sh#13,#40`, `lock-held.sh`. Mark: CHANGES (C-LOCK).

**FP-2**
- The stop file ends the loop `stopped on request` before the next worker, and is consumed.
- Source: `loop.sh:414`. Test: `loop.sh#14`. Mark: CHANGES (`fanin pause`).

**FP-3**
- `ISSUE #n` from an analysis: close the pane and end `done: analysis needs-owner <url>`.
- `NOTHING`: close the pane and end `idle`.
- `BLOCKED`: notify and end `blocked`, keeping the pane.
- Source: `loop.sh:529-560`. Test: `loop.sh` (analysis cases). Mark: CHANGES (C-REPORT).

**FP-4**
- Remote repos run through `ssh -o BatchMode=yes … bash -s` with the lock fds closed. A Git LFS repo without
  git-lfs runs in its `nix develop` shell.
- Source: `lib.sh in_repo, lfs_shell`. Test: `remote.sh#5`, `oc-pr.sh#5`, `review.sh#4`. Mark: CHANGES.
  The worker host becomes a result watcher; the host runner owns remote git.

**FP-5**
- `ORCH_DEBUG=1` names every failing command and does not stop the script.
- Source: `lib.sh`. Test: `lib-helpers.sh#7`. Mark: CHANGES (Go: wrapped errors with `%w` plus structured logs).

**FP-6**
- Every herdr, gh and MCP read that fails ends in a named error, never a default verdict:
  - The gate (GH-5, GT-6).
  - The picker (PK-4).
  - Spawn (SP-5).
  - Nudge (AW-13).
  - Await (AW-6).
- Mark: PARITY. This is the single most important invariant.

**FP-7**
- Closing finished agents, then empty tabs and workspaces, never touches a tab where another pane, agent or owner
  session runs.
- Source: `CLAUDE.md`, `overview.sh --close-done`. Test: `overview.sh#5`. Mark: PARITY (the adapter closes the run
  pane on a terminal state, ADDENDUM §2.10.1-1).

## 18. Shadow import

`fanind --shadow DIR` mirrors the bash state into the store during migration step 3 ([spec](spec/shadow.md)).

**SH-1**
- Given: `state/<label>.md` with spawn, pr, blocked, ready, paused, merged and idle lines.
- Then: one task per worker agent with one run, in the state its newest line sets; a new spawn cancels the
  lane's open task; lines of helpers and notes are skipped.
- Source: `status.sh`, `lib.sh EVENT_RE`. Test: `status.sh`. Mark: CHANGES (C-STORE).

**SH-2**
- Given: `.<label>.merge-hold` or `.<label>.ci-hold` (with `ci-fix: <agent>`).
- Then: an active `merge` or `ci` hold with line 1 as reason and the fix agent's task; a removed file lifts it.
- Source: `lib.sh merge_hold, ci_hold`. Test: `status.sh`. Mark: CHANGES (C-STORE).

**SH-3**
- Given: `.<label>.merge.lock` under a flock.
- Then: the project lock, held by the PR's task, renewed each import; a free flock releases it.
- Source: `status.sh merge_field`. Test: `status.sh`. Mark: CHANGES (C-LOCK).

**SH-4**
- Given: a missing directory, an unreadable or non-UTF-8 file, a held lock with bad content, or no `/proc/locks`.
- Then: `shadow.ErrUnreadable` naming the file, and the store unchanged; never an empty status.
- Source: —. Test: —. Mark: CHANGES (FP-6).

**SH-5**
- The importer never writes to the state directory: it is byte for byte the same after an import.
- An import of an unchanged directory writes no row and no event.
- Source: —. Test: —. Mark: CHANGES (C-STORE).

**SH-6**
- `fanind --shadow DIR` imports before it serves and then every interval; `fanin status` shows the imported
  projects. A failed first import stops `fanind` with exit 1.
- Source: —. Test: —. Mark: CHANGES (C-STORE).

## 19. Intake sources

Webhooks, cron and the release webhook queue the same `intake.TaskRequest` as the CLI ([spec](spec/intake.md)).

**IN-1**
- A delivery to `POST /api/v1/webhooks/<name>` without the HMAC-SHA256 signature of its secret, or with a wrong
  one, is 401 and queues nothing. An unknown name is 404. The route needs no owner token, and a webhook delivery
  to a command endpoint is refused. An unset secret stops the start.
- Source: —. Test: `internal/api/webhook_routes_test.go`, `internal/intake/webhook_test.go`, `cmd/fanind/intake_test.go`. Mark: CHANGES (C-LOOP).

**IN-2**
- Forge webhook: an issue that carries the configured label queues one owner task `<project>-issue-<n>`; a
  redelivery queues nothing more. Other events and issues without the label are ignored.
- Source: —. Test: `internal/intake/webhook_test.go`, `cmd/fanind/intake_test.go`. Mark: CHANGES (C-LOOP).

**IN-3**
- Alert webhook: a firing Alertmanager group queues one analysis; a repeat of the same group and start queues
  nothing more; a resolved group is ignored.
- Source: —. Test: `internal/intake/webhook_test.go`. Mark: CHANGES (C-LOOP).

**IN-4**
- Cron: each entry is a `timers` row; every due queues its task exactly once, also across a restart, and arms
  the next due.
- Source: —. Test: `internal/intake/cron_test.go`. Mark: CHANGES (C-LOCK).

**IN-5**
- Release webhook: a published GitHub release starts DR-1 for the webhook's project; any other event or action is
  ignored.
- Source: `deploy-release.sh`. Test: `internal/intake/webhook_test.go`. Mark: CHANGES (C-LOOP).

## 20. Observability

Metrics, `fanin doctor` and structured logs ([spec](spec/observability.md)).

**OB-1**
- `GET /metrics` (no token) serves the Prometheus text format: tasks and runs per project and state, and the
  counters of runs started, nudges, merges and adapter errors. A store that cannot be read is a 500, never partial
  numbers.
- Source: —. Test: `internal/metrics/metrics_test.go`, `internal/api/metrics_route_test.go`. Mark: CHANGES (C-STORE).

**OB-2**
- Every number is counted from the store, so a store transition raises its counter in the same commit and a
  restart loses nothing.
- Source: —. Test: `internal/metrics/metrics_test.go`. Mark: CHANGES (C-STORE).

**OB-3**
- `fanin doctor` prints one row per check: config, store, herdr, forge auth and branch protection per project
  (`fanin/review` required), systemd. A component that does not answer is a FAIL row and exit 1, never ok. A check
  that cannot run here (a project on a remote host) is a `skip` with its reason. No token is printed.
- Source: —. Test: `internal/doctor/doctor_test.go`, `cmd/fanin/cmd_doctor_test.go`. Mark: CHANGES (C-CONFIG).

**OB-4**
- Every task-bound log line of the scheduler carries `project`, `task_id` and `run_id`.
- Source: —. Test: `internal/sched/logids_test.go`. Mark: CHANGES (C-LOG).

## Coverage gaps today

These have no test and need one in fanin:

- **Spawn's tab title restore** after a resume.

Write the tests for the failure paths of these first.
