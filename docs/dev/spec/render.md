# Render spec

`internal/render` writes one Markdown status log per project from the store (concept D3, C-STORE). The file is a
window; the store keeps everything. Nothing is ever read back from the Markdown.

## Window (SCENARIOS RS-1)

`Renderer.Render(ctx, project)` shows, in this order:

| Part | Content |
|---|---|
| Header | project, render time from the injected `Clock`, task and active hold counts, one line per hold |
| Ready | tasks in `gating` or `post_merge` |
| Blocked | the newest (by `UpdatedAt`) task in `needs_owner` or `paused` |
| Queued | every `queued` task |
| Owner decisions | questions asked within `render.DecisionWindow` (7 days) of now, open or answered |
| Current run | the newest unfinished run of an open task, with slot, branch and budget |

Everything older stays in the store and is read through `store.ProjectTasks`, `ProjectQuestions` and `Events`.
There is no per-lane split yet: lanes do not exist in the model, so the blocked entry is per project.

## Atomic writes (SCENARIOS RS-2)

`Renderer.Write(ctx, project, path)` renders first, then calls `atomicfile.Write`: a temp file in the target
directory, `fsync`, rename over the target, `fsync` of the directory. Any failure removes the temp file and leaves
the old file byte for byte. A reader sees the old or the new file, never a mix.

The store gained the reads the window needs: `ProjectTasks`, `ProjectQuestions` and `Projects`.
