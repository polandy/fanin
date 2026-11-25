Hard limits. Breaking one can take down live hosts or leak secrets:
- Never run `nixos-rebuild`, `switch`, a deploy, or anything with `sudo`.
- Never open, decrypt, print or grep anything under `secrets/`, `~/.ssh` or any key or token file.
- Never `ssh`/`scp` to any other host and never run `herdr`.
- Never push to `{{BASE}}`, never rebase or force-push a feature branch (update it with `git merge origin/{{BASE}}`), never touch the main checkout or other worktrees.
