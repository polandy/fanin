# Operation

## Starting fanind

```sh
export FANIN_OWNER_TOKEN=...        # the name set in owner_token_env
fanind --config /path/to/fanin.yaml
```

`fanind` opens (or creates) the store, serves the API on `listen` and logs `fanind: serving` with the address.
It exits with 1 and a message naming the fix when the config is missing or invalid, the token variable is empty,
or the address is taken.

SIGINT or SIGTERM stops it: open requests end, the store is closed and the exit code is 0. A stop changes no task.

## fanin status

```sh
fanin --daemon-url http://127.0.0.1:7420 status          # every project, in config order
fanin --daemon-url http://127.0.0.1:7420 status alpha    # only alpha
```

`--daemon-url` falls back to `$FANIN_URL`. Each line shows:

- `loop=active|idle`: whether a task of the project is running, in its gates or in post-merge.
- `worker=<n>`: the workers still running.
- `merge=free`, `held by <holder>` or `stale (<holder>)`: the project lock. `stale` means the lease ran out or its
  holder is gone; the lock is free to take.
- `last:` the newest event of the project.
- `open:` ready, blocked, paused, needs-owner and failed tasks, and the active holds.

A daemon that cannot be reached is an error (exit 1), never an empty status.
