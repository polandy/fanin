#!/usr/bin/env bash
# Usage: check-scenarios.sh --report|--strict [scenarios.md [dir...]]
# Reads scenario IDs (**XX-n**) from the scenarios file and looks for them in *_test.go files:
# test names (TestX/GH-2_..., t.Run("GH-2_...")) and comments (see SCENARIOS GH-2).
# --report lists missing IDs per section and exits 0; --strict exits 1 when any ID is missing.
# An ID cited in a test that the scenarios file does not define fails in both modes.
# SCENARIO_EXCEPTIONS (default scripts/scenario-exceptions.txt, only for docs/dev/scenarios.md) lists "ID<TAB>reason" for behaviour that is retired
# or lives outside this repo; an excepted ID counts as covered. An exception for an unknown or tested ID fails.
set -euo pipefail
export LC_ALL=C

mode=${1:-}
case $mode in
  --report | --strict) shift ;;
  *) echo "usage: $0 --report|--strict [scenarios.md [dir...]]" >&2; exit 2 ;;
esac
scenarios=${1:-docs/dev/scenarios.md}
if [ $# -gt 0 ]; then shift; fi
if [ $# -eq 0 ]; then
  for d in cmd internal pkg; do [ -d "$d" ] && set -- "$@" "$d"; done
fi
for d in "$@"; do [ -d "$d" ] || { echo "check-scenarios: $d is not a directory; pass the Go source dirs to scan" >&2; exit 2; }; done
[ -r "$scenarios" ] || { echo "check-scenarios: cannot read $scenarios; pass the scenarios file as argument 2" >&2; exit 2; }

id='[A-Z]+-[0-9]+'
cited=$(find "$@" \( -name vendor -o -name testdata -o -name .git \) -prune -o -name '*_test.go' -print0 |
  xargs -0 -r grep -hoE "(Test[A-Za-z0-9_]*/|t\.Run\(\"|see SCENARIOS )$id([, /]+$id)*" |
  grep -oE "(^|[^A-Z])$id" | grep -oE "$id" | sort -u || true)

# Section lines: "<section>\t<ID>"; the section is the "## n. title" heading.
defined=$(awk '/^## [0-9]+\. /{s=substr($0,4)} /^\*\*[A-Z]+-[0-9]+\*\*([ :].*)?$/{sub(/\*\*[ :].*$/,"**")} /^\*\*[A-Z]+-[0-9]+\*\*$/{gsub(/\*/,""); print s "\t" $0}' "$scenarios" | awk '!seen[$0]++')
[ -n "$defined" ] || { echo "check-scenarios: no scenario IDs found in $scenarios" >&2; exit 2; }

default_exc=; [ "$scenarios" = docs/dev/scenarios.md ] && default_exc=scripts/scenario-exceptions.txt
exc_file=${SCENARIO_EXCEPTIONS:-$default_exc}
excepted=""
if [ -n "$exc_file" ] && [ -r "$exc_file" ]; then
  excepted=$(grep -vE '^[[:space:]]*(#|$)' "$exc_file" | cut -f1 | sort -u || true)
elif [ -n "${SCENARIO_EXCEPTIONS:-}" ]; then
  echo "check-scenarios: cannot read $exc_file; fix SCENARIO_EXCEPTIONS" >&2; exit 2
fi
stale=$(comm -12 <(printf '%s\n' "$excepted" | sed '/^$/d') <(printf '%s\n' "$cited" | sed '/^$/d'))
bad=$(comm -23 <(printf '%s\n' "$excepted" | sed '/^$/d') <(cut -f2 <<<"$defined" | sort -u))
if [ -n "$stale$bad" ]; then
  echo "check-scenarios: $exc_file excepts IDs that have a test or that $scenarios does not define: $(tr '\n' ' ' <<<"$stale$bad")" >&2
  echo "remove the exception line" >&2
  exit 1
fi
cited=$(printf '%s\n%s\n' "$cited" "$excepted" | sed '/^$/d' | sort -u)

unknown=$(comm -23 <(printf '%s\n' "$cited" | sed '/^$/d') <(cut -f2 <<<"$defined" | sort -u))
if [ -n "$unknown" ]; then
  echo "check-scenarios: tests cite IDs that $scenarios does not define: $(tr '\n' ' ' <<<"$unknown")" >&2
  echo "fix the ID in the test or add the scenario to $scenarios" >&2
fi

report=$(awk -F'\t' '
  FILENAME == ARGV[1] { have[$0] = 1; next }
  { if (!($1 in total)) order[++k] = $1; total[$1]++; if (!($2 in have)) miss[$1] = miss[$1] " " $2 }
  END { for (i = 1; i <= k; i++) { s = order[i]; m = split(miss[s], a, " ");
    printf "%s: %d of %d covered", s, total[s] - m, total[s]; if (m) printf ", missing:%s", miss[s]; print "" } }
' <(printf '%s\n' "$cited") - <<<"$defined")
missing=$(grep -c ': [0-9]* of [0-9]* covered, missing:' <<<"$report" || true)

echo "$report"
[ -z "$unknown" ] || exit 1
if [ "$mode" = --strict ] && [ "$missing" -gt 0 ]; then
  echo "check-scenarios: $missing section(s) have scenarios without a test" >&2
  exit 1
fi
