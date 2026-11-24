#!/usr/bin/env bash
# Fails when tracked files (or, with --history, any commit diff) contain private data.
set -uo pipefail

usage() { echo "usage: $0 [--history]" >&2; exit 2; }
mode=tree
case "${1:-}" in
  "") ;;
  --history) mode=history ;;
  *) usage ;;
esac

cd "$(git rev-parse --show-toplevel)" || exit 2

patterns=(
  '\b(10\.[0-9]{1,3}|192\.168|172\.(1[6-9]|2[0-9]|3[01])|100\.(6[4-9]|[7-9][0-9]|1[01][0-9]|12[0-7]))\.[0-9]{1,3}\.[0-9]{1,3}\b'
  '\b169\.254\.[0-9]{1,3}\.[0-9]{1,3}\b'
  '/home/[A-Za-z0-9_]'
  '/Users/[A-Za-z0-9_]'
  '[A-Za-z]:\\Users\\'
  '\b[A-Za-z0-9-]+\.(lan|local|localdomain|home|internal|intranet|fritz\.box)\b'
  '\b[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}\b'
)
# Emails that may appear: examples, GitHub noreply addresses and ssh remotes.
allow='@(example\.(com|org|net)|users\.noreply\.github\.com)\b|\bgit@github\.com\b|\bnoreply@github\.com\b'

# Extra terms that must not be published themselves: one per line in .private-terms (gitignored) or in
# FANIN_PRIVATE_TERMS (newline or comma separated).
extra=()
if [[ -f .private-terms ]]; then
  while IFS= read -r t; do [[ -n "$t" && "$t" != \#* ]] && extra+=("$t"); done < .private-terms
fi
if [[ -n "${FANIN_PRIVATE_TERMS:-}" ]]; then
  while IFS= read -r t; do [[ -n "$t" ]] && extra+=("$t"); done < <(tr ',' '\n' <<<"$FANIN_PRIVATE_TERMS")
fi

regex=$(IFS='|'; echo "${patterns[*]}")

scan() {
  if [[ $mode == tree ]]; then
    git grep --untracked -nIE "$1" -- . ':!scripts/check-private-data.sh'
  else
    git log --all -p --format='commit %H' -- . ':!scripts/check-private-data.sh' | grep -nE "$1"
  fi
}

hits=$(scan "$regex" | grep -vE "$allow")
for t in "${extra[@]}"; do
  more=$(if [[ $mode == tree ]]; then git grep --untracked -nIiF -e "$t" -- . ':!.private-terms'; \
    else git log --all -p --format='commit %H' | grep -niF -e "$t"; fi)
  [[ -n "$more" ]] && hits+=$'\n'"$more"
done
hits=$(sed '/^$/d' <<<"$hits")

if [[ -n "$hits" ]]; then
  echo "private data found ($mode):" >&2
  echo "$hits" >&2
  exit 1
fi
echo "no private data found ($mode)"
