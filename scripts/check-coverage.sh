#!/usr/bin/env bash
# Fails when a package's statement coverage in a Go cover profile is below the threshold.
# A package without profile entries (not written yet) is skipped.
# usage: check-coverage.sh <cover.out> <min-percent> <pkg-dir>...   (e.g. internal/gate)
set -uo pipefail

[ $# -ge 3 ] || { echo "usage: $0 <cover.out> <min-percent> <pkg-dir>..." >&2; exit 2; }
profile=$1
min=$2
shift 2
[ -f "$profile" ] || { echo "check-coverage: $profile not found; run go test -coverprofile first" >&2; exit 2; }

fail=0
for pkg in "$@"; do
  read -r total covered < <(awk -v pkg="$pkg" '
    NR > 1 {
      split($1, a, ":"); file = a[1]; sub(/\/[^\/]*$/, "", file)
      if (file == pkg || substr(file, length(file) - length(pkg)) == "/" pkg) {
        total += $2; if ($3 > 0) covered += $2
      }
    }
    END { print total + 0, covered + 0 }' "$profile")
  if [ "$total" -eq 0 ]; then
    echo "check-coverage: $pkg has no statements in $profile, skipped"
    continue
  fi
  pct=$(awk -v c="$covered" -v t="$total" 'BEGIN { printf "%.1f", 100 * c / t }')
  if awk -v p="$pct" -v m="$min" 'BEGIN { exit !(p < m) }'; then
    echo "check-coverage: $pkg covers $pct% of statements, below $min%" >&2
    fail=1
  else
    echo "check-coverage: $pkg $pct% (min $min%)"
  fi
done
exit "$fail"
