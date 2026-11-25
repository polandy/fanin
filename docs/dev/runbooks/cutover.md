# Cutover: retiring the bash loops

Status: runbook for migration step 8 ([concept](../concept.md#migration)). The fanin side is described here and can be
done from this repo. Stopping the bash loops, the Stop hook and `loop-all.sh` happens in the private orchestrator repo;
this page only says what to do there and what to check.

Do one project at a time, low-risk projects first. Every step has a check; do not go on when a check fails, go to
[Rollback](#rollback).

## Before you start

- `fanind` runs on the control host with `--shadow` pointing at the bash state directory, and `fanin status` shows the
  project as the bash loop left it ([operation](../../user/operation.md#following-the-bash-orchestrator)).
- `fanin doctor` is clean for the project: config, store, herdr, forge login and the `fanin/review` branch protection.
- The project has no task in flight in the bash loop. `fanin status <project>` shows no `running` or `blocked` task and
  no active hold under `open:`; lift or finish what is there first.

## 1. Take the project into fanin

1. In `config/projects/<project>.yaml` set `loop: fanin` ([configuration](../../user/configuration.md)). Leave
   everything else as the bash path used it.
2. `fanin config effective <project>` shows the values the daemon will use; compare base branch, host, worker kind and
   merge settings with the project's state header.
3. Restart `fanind`. It exits with 1 and a message naming the key when the config is invalid.

Check: `fanind: serving` in the log, `fanin doctor` is clean, and `fanin status <project>` still lists the old
`shadow-` tasks as history. From now on the importer skips the project (`Importer.Skip`), so its bash state is not
imported again; the new tasks have no `shadow-` prefix.

## 2. Stop the bash loop for the project

In the private repo:

1. Stop the project's `loop.sh` (and any lane loops) and remove the project from `loop-all.sh`'s list, so nothing
   restarts it.
2. Leave the state file and `archive/` in place; they are the record for a rollback.

Check: no `loop.sh` process for the project (`pgrep -af loop.sh`), its `.merge.lock` is not held, and `fanin status
<project>` shows no lock held by the shadow importer after one import interval (30 s).

## 3. Prove one task end to end

Queue one small task (`fanin task add`, a forge label or a cron entry) and watch it:

- the daemon starts the worker, the worker reports through `fanin_report` or `fanin report`,
- the gate reads the PR, the review status is posted, and a green PR merges without the owner,
- `fanin overview <project>` shows the task under done.

Check: the task ends `done` with its merge in the event list. A red check that fanin nudges counts as a pass; a
task that stops with an unreadable gate or forge answer is an error to fix, not to retry.

## 4. Switch the shadow importer off

Once every project runs with `loop: fanin`, the importer has nothing left to import:

1. Remove `--shadow` from the `fanind` unit (or unset `FANIN_SHADOW`).
2. Restart `fanind`.

Check: `fanind` starts without a shadow import line, and `fanin status` is unchanged for every project. While a
project is still on `loop: bash`, keep the importer on; it is the only way `fanin status` shows that project.

## 5. Retire the owner-session machinery

In the private repo, after the last project has moved:

1. Remove the Stop hook (`watch-agents.sh check`) from `.claude/settings.json`, so the owner's reply is no longer
   refused for unwatched agents.
2. Stop `watch-agents.sh` and retire `loop-all.sh`; the owner session answers through `fanin` (CLI, MCP, UI) and reads
   `fanin overview`.
3. Freeze the bash scripts for good. Nothing new goes there.

Check: the owner session ends a reply with a worker running, `fanin overview` lists the question or the PR waiting for
the owner, and `fanin_status` over MCP answers.

Behaviour that ends here and has no fanin equivalent is listed in `scripts/scenario-exceptions.txt`: the watcher and
its lock (WA-1, WA-4 to WA-6), `loop-all.sh` recipes (OA-4, OA-5), lanes and the chain templates (CH-4, CH-5),
remote `ssh … bash -s` runs (FP-4) and `firewall-diff` (GT-9, stays a project's `pre_go_check`).

## Rollback

Each project rolls back on its own, as long as the bash state and scripts are still there (step 5 not done).

1. Stop new work: `fanin pause <project>` stops the project before its next worker. Answer or finish the running
   tasks; a running worker keeps its pane.
2. In the project's YAML set `loop: bash` and restart `fanind`. With `--shadow` on, the importer follows the project
   again.
3. In the private repo start `loop.sh` for the project and add it back to `loop-all.sh`. Re-add the Stop hook if the
   other projects still rely on it.

Check: `fanin status <project>` shows the bash state again after one import interval, and the bash loop's first
`idle` or `done` line comes back. Tasks fanin finished stay in the store as history; the bash state file does not know
them.

If step 5 is already done, restore the hook and `loop-all.sh` from the private repo's history first, then do 2 and 3.
