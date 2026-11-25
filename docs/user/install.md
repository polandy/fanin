# Install

## What you need

- A control host that runs `fanind`, with systemd.
- The forge tokens and one owner token (see [Secrets](#secrets)).
- Every worker host must reach the control host's API at `listen`, and the control host every worker host. The
  default `127.0.0.1:7420` serves the control host only; for workers elsewhere set `listen` to a private
  address and keep it off the internet.

## Build the binaries

With Go 1.26:

```sh
make build                      # writes bin/fanind and bin/fanin
sudo install -m 0755 bin/fanind bin/fanin /usr/local/bin/
```

With nix:

```sh
nix build github:polandy/fanin  # result/bin/fanind and result/bin/fanin
```

## Directories

```sh
sudo useradd --system --home-dir /var/lib/fanin --shell /usr/sbin/nologin fanin
sudo install -d -o root -g fanin -m 0750 /etc/fanin
sudo cp -r config/examples/. /etc/fanin/
```

- `/etc/fanin` is the config directory (see [Configuration](configuration.md)). Edit `hosts.yaml`, `forges.yaml`
  and `projects/` for your setup, and set `store: /var/lib/fanin/fanin.db` in `fanin.yaml`.
- `/var/lib/fanin` is created by the unit (`StateDirectory=fanin`) and holds the SQLite store.

```sh
fanin config validate /etc/fanin   # prints ok with the project count
```

## Secrets

The config names secrets, it never holds them. Put them in `/etc/fanin/fanind.env`, which only root reads:

```sh
sudo install -m 0600 /dev/null /etc/fanin/fanind.env
sudoedit /etc/fanin/fanind.env
```

```
FANIN_OWNER_TOKEN=...
GH_TOKEN=...
GITEA_TOKEN=...
```

The names are the ones `owner_token_env` and each forge's `token_env` set. `fanind` refuses to start while the
owner token is empty and says which variable it looked for.

## Enable the unit

```sh
sudo cp deploy/systemd/fanind.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now fanind
systemctl status fanind
journalctl -u fanind -n 20      # shows "fanind: serving" with the address
```

systemd starts the unit with a bare environment: only `PATH` from the unit, the variables of the
`EnvironmentFile`, and `--config /etc/fanin`. Nothing is read from a home directory or a login shell. Adjust the
`PATH` line when the tools your workers need live elsewhere.

## Opencode sandbox

Opencode workers run as the sandbox user `aiagent` in its clone `/srv/aiagent/<project>`; fanind calls `sudo -n`,
so a password prompt is an error that names the missing line. Give the daemon's user these NOPASSWD commands, and
add the sandbox clone to each opencode project's checkout as the remote `aiagent`:

```sh
# /etc/sudoers.d/fanin-opencode (edit with visudo -f)
fanin ALL=(aiagent) NOPASSWD: /usr/bin/git, /bin/sh, /usr/local/bin/oc-sandbox
git -C <project checkout> remote add aiagent /srv/aiagent/<project>
```

fanind writes the launcher shim to `oc-bin/` next to the store. Leftovers in a sandbox clone stop the next spawn
("is not clean"): move them out, never delete them.

## Branch protection

fanin merges a PR only after both required checks are green. Set them as required on the base branch of every
project.

GitHub: Settings → Branches → Add branch ruleset (or classic rule) for `main` → Require status checks to pass →
add `ci` and `fanin/review`; also require the branch to be up to date and block force pushes.

Gitea or Forgejo: Settings → Branches → Add rule for `main` → Enable status check → add `ci` and `fanin/review`
as patterns; disable force push.

`ci` is your CI job; `fanin/review` is the status the review step reports. `fanin doctor` reads the classic rule
only, so a ruleset shows as a `FAIL` row. A check that never reports keeps the
PR blocked (Gitea projects end in `NOCI` after `gate.gitea.noci_after_polls` polls without a status).

## Upgrade

Install the new binaries, then `sudo systemctl restart fanind`. A restart changes no task.
