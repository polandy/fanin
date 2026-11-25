#!/usr/bin/env bash
# Fails when a Markdown file under the user docs links into docs/dev.
set -uo pipefail

dir=${1:-docs/user}
[[ -d $dir ]] || { echo "check-docs-links: no such directory: $dir" >&2; exit 2; }

bad=$(grep -rnE --include='*.md' '\]\([^)]*(\.\./dev/|docs/dev/|\.\./dev\)|docs/dev\))|^\[[^]]+\]:[[:space:]]*\S*(\.\./dev/|docs/dev/)' "$dir")
case $? in
  0) echo "check-docs-links: user docs must not link into docs/dev; move the text or drop the link:" >&2
     echo "$bad" >&2
     exit 1 ;;
  1) exit 0 ;;
  *) echo "check-docs-links: grep failed on $dir" >&2; exit 2 ;;
esac
