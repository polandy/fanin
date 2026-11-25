#!/usr/bin/env bash
# Fails when a non-test .go file exceeds the line limit and is not on the exception list.
# usage: check-file-size.sh <max-lines> <exceptions-file> [dir...]   (default dirs: cmd internal pkg)
set -uo pipefail

[ $# -ge 2 ] || { echo "usage: $0 <max-lines> <exceptions-file> [dir...]" >&2; exit 2; }
max=$1
exceptions=$2
shift 2
dirs=("$@")
[ ${#dirs[@]} -eq 0 ] && dirs=(cmd internal pkg)

fail=0
while IFS= read -r f; do
  lines=$(wc -l < "$f")
  [ "$lines" -le "$max" ] && continue
  if [ -f "$exceptions" ] && grep -qxF "$f" <(grep -vE '^[[:space:]]*(#|$)' "$exceptions"); then
    continue
  fi
  echo "check-file-size: $f has $lines lines (limit $max); split it or list it in $exceptions" >&2
  fail=1
done < <(find "${dirs[@]}" -name '*.go' -not -name '*_test.go' 2>/dev/null | sort)
exit "$fail"
