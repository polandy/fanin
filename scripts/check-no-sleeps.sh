#!/usr/bin/env bash
# Fails when time.Sleep, time.After or a timer or ticker constructor appears in Go code outside internal/clock.
# usage: check-no-sleeps.sh [dir...]   (default: cmd internal pkg)
set -uo pipefail

dirs=("$@")
[ ${#dirs[@]} -eq 0 ] && dirs=(cmd internal pkg)
existing=()
for d in "${dirs[@]}"; do [ -d "$d" ] && existing+=("$d"); done
[ ${#existing[@]} -eq 0 ] && exit 0

found=$(grep -rnE 'time\.(Sleep|After|AfterFunc|NewTimer|NewTicker|Tick)\(' --include='*.go' "${existing[@]}")
[ $? -gt 1 ] && { echo "check-no-sleeps: grep failed" >&2; exit 2; }
hits=$(grep -vE '(^|/)internal/clock/' <<< "$found")
if [ -n "$hits" ]; then
  echo "check-no-sleeps: time.Sleep, time.After and timers are only allowed in internal/clock; inject a clock instead:" >&2
  echo "$hits" >&2
  exit 1
fi
