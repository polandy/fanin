#!/usr/bin/env bash
# Fails when the tree (or, with --history, any line ever added on any ref) contains private data.
set -uo pipefail

die() { echo "check-private-data: $*" >&2; exit 2; }

mode=tree
case "${1:-}" in
  "") ;;
  --history) mode=history ;;
  *) die "usage: $0 [--history]" ;;
esac

top=$(git rev-parse --show-toplevel) || die "not inside a git repository"
cd "$top" || die "cannot cd to $top"
self=scripts/check-private-data.sh

patterns=(
  '(^|[^0-9.])(10\.[0-9]{1,3}|192\.168|172\.(1[6-9]|2[0-9]|3[01])|100\.(6[4-9]|[7-9][0-9]|1[01][0-9]|12[0-7])|169\.254)\.[0-9]{1,3}\.[0-9]{1,3}([^0-9.]|$)'
  '/home/[A-Za-z0-9_]'
  '/Users/[A-Za-z0-9_]'
  '[A-Za-z]:[\\/]Users[\\/]'
  '[A-Za-z0-9-]\.(lan|local|localdomain|home\.arpa|fritz\.box)([^A-Za-z0-9.-]|$)'
  '[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}'
)
regex=$(IFS='|'; echo "${patterns[*]}")
# Allowed: @2x image names, example and GitHub noreply addresses, ssh remotes. Replaced by a space before matching.
allow='@[0-9]+x\.(png|svg|jpg|webp)|[A-Za-z0-9._%+-]*@(example\.(com|org|net)|users\.noreply\.github\.com)|git@github\.com|noreply@github\.com'

# Terms that must not be published themselves: one per line in .private-terms (gitignored) or in
# FANIN_PRIVATE_TERMS (newline or comma separated).
terms=$(mktemp) || die "mktemp failed"
trap 'rm -f "$terms"' EXIT
if [[ -f .private-terms ]]; then
  grep -vE '^[[:space:]]*(#|$)' .private-terms >>"$terms"
  [[ $? -le 1 ]] || die "cannot read .private-terms"
fi
if [[ -n "${FANIN_PRIVATE_TERMS:-}" ]]; then
  tr ',' '\n' <<<"$FANIN_PRIVATE_TERMS" | grep -v '^[[:space:]]*$' >>"$terms"
fi

# Prints candidate lines as "<where>:<text>": the tree as file:line, the history as commit:file.
candidates() {
  if [[ $mode == tree ]]; then
    git grep --untracked -nI -e '' -- . ":!$self" ':!.private-terms'
    [[ $? -le 1 ]] || die "git grep failed"
  else
    git log --all -p --no-color --format='commit %H' -- . ":!$self" | awk '
      /^commit [0-9a-f]+$/ { c = substr($2, 1, 12); next }
      /^\+\+\+ / { f = substr($0, 5); sub(/^b\//, "", f); next }
      /^\+/ { print c ":" f ":" substr($0, 2) }'
    [[ ${PIPESTATUS[0]} -eq 0 ]] || die "git log failed"
  fi
}

lines=$(candidates) || exit 2
hits=$(sed -E "s/$allow/ /g" <<<"$lines" | grep -E "$regex")
if [[ -s "$terms" ]]; then
  hits+=$'\n'$(grep -iF -f "$terms" <<<"$lines")
fi
hits=$(sed '/^$/d' <<<"$hits")

if [[ -n "$hits" ]]; then
  echo "private data found ($mode):" >&2
  echo "$hits" >&2
  exit 1
fi
echo "no private data found ($mode)"
