#!/usr/bin/env bash
# Usage: check-scenarios.sh --report|--strict [scenarios.md [dir...]]
# Reads scenario IDs (**XX-n**) from the scenarios file and looks for them in *_test.go files:
# test names (TestX/GH-2_..., t.Run("GH-2_...")) and comments (see SCENARIOS GH-2).
# --report lists missing IDs per section and exits 0; --strict exits 1 when any ID is missing.
# An ID cited in a test that the scenarios file does not define fails in both modes.
set -euo pipefail

mode=${1:-}
case $mode in
  --report | --strict) shift ;;
  *) echo "usage: $0 --report|--strict [scenarios.md [dir...]]" >&2; exit 2 ;;
esac
scenarios=${1:-docs/dev/scenarios.md}
[ $# -gt 0 ] && shift
[ $# -gt 0 ] || set -- .
[ -r "$scenarios" ] || { echo "check-scenarios: cannot read $scenarios; pass the scenarios file as argument 2" >&2; exit 2; }

id='[A-Z]{2,3}-[0-9]+'
cited=$(find "$@" \( -name vendor -o -name testdata -o -name .git \) -prune -o -name '*_test.go' -print0 |
  xargs -0 -r grep -hoE "(Test[A-Za-z0-9]*/|t\.Run\(\"|see SCENARIOS )$id([, ]+$id)*" |
  grep -oE "$id" | sort -u || true)

# Section lines: "<section>\t<ID>"; the section is the "## n. title" heading.
defined=$(awk '/^## [0-9]+\. /{s=substr($0,4)} /^\*\*[A-Z]+-[0-9]+\*\*$/{gsub(/\*/,""); print s "\t" $0}' "$scenarios")
[ -n "$defined" ] || { echo "check-scenarios: no scenario IDs found in $scenarios" >&2; exit 2; }

unknown=$(comm -23 <(printf '%s\n' "$cited" | sed '/^$/d') <(cut -f2 <<<"$defined" | sort -u))
if [ -n "$unknown" ]; then
  echo "check-scenarios: tests cite IDs that $scenarios does not define: $(tr '\n' ' ' <<<"$unknown")" >&2
  echo "fix the ID in the test or add the scenario to $scenarios" >&2
fi

report=$(awk -F'\t' -v cited="$cited" '
  BEGIN { n = split(cited, c, "\n"); for (i = 1; i <= n; i++) have[c[i]] = 1 }
  { if (!($1 in total)) order[++k] = $1; total[$1]++; if (!($2 in have)) miss[$1] = miss[$1] " " $2 }
  END { for (i = 1; i <= k; i++) { s = order[i]; m = split(miss[s], a, " ");
    printf "%s: %d of %d covered", s, total[s] - m, total[s]; if (m) printf ", missing:%s", miss[s]; print "" } }
' <<<"$defined")
missing=$(grep -c 'missing:' <<<"$report" || true)

echo "$report"
[ -z "$unknown" ] || exit 1
if [ "$mode" = --strict ] && [ "$missing" -gt 0 ]; then
  echo "check-scenarios: $missing section(s) have scenarios without a test" >&2
  exit 1
fi
