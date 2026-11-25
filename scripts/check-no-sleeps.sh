#!/usr/bin/env bash
# Fails when time.Sleep or time.After appears in Go code outside internal/clock.
# usage: check-no-sleeps.sh [dir...]   (default: cmd internal pkg)
set -uo pipefail

dirs=("$@")
[ ${#dirs[@]} -eq 0 ] && dirs=(cmd internal pkg)
existing=()
for d in "${dirs[@]}"; do [ -d "$d" ] && existing+=("$d"); done
[ ${#existing[@]} -eq 0 ] && exit 0

hits=$(grep -rnE 'time\.(Sleep|After)\(' --include='*.go' "${existing[@]}" | grep -vE '(^|/)internal/clock/')
if [ -n "$hits" ]; then
  echo "check-no-sleeps: time.Sleep and time.After are only allowed in internal/clock; inject a clock instead:" >&2
  echo "$hits" >&2
  exit 1
fi
