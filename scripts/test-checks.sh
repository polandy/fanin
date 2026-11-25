#!/usr/bin/env bash
# Negative tests: each check script must fail on a bad input and pass on a good one.
set -uo pipefail

cd "$(git rev-parse --show-toplevel)" || exit 2
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
failures=0

expect() { # expect <pass|fail> <name> <command...>
  local want=$1 name=$2
  shift 2
  "$@" >/dev/null 2>&1
  case $? in 0) got=pass ;; 1) got=fail ;; *) got=error ;; esac
  if [ "$got" = "$want" ]; then echo "ok   $name"; else echo "FAIL $name: wanted $want, got $got" >&2; failures=$((failures + 1)); fi
}

# check-no-sleeps
expect fail "no-sleeps rejects time.Sleep in a test" scripts/check-no-sleeps.sh scripts/testdata/sleeps
mkdir -p "$tmp/clock/internal/clock"
printf 'package clock\n\nimport "time"\n\nfunc Wait() { <-time.After(time.Second) }\n' > "$tmp/clock/internal/clock/clock.go"
expect pass "no-sleeps allows time.After in internal/clock" scripts/check-no-sleeps.sh "$tmp/clock"
expect pass "no-sleeps passes on the repo tree" scripts/check-no-sleeps.sh

# check-file-size
mkdir -p "$tmp/size"
for n in 400 401; do
  { echo "package big"; for ((i = 2; i <= n; i++)); do echo "// line $i"; done; } > "$tmp/size/f$n.go"
done
: > "$tmp/none.txt"
expect fail "file-size rejects 401 lines" scripts/check-file-size.sh 400 "$tmp/none.txt" "$tmp/size/f401.go"
mkdir -p "$tmp/size400"
mv "$tmp/size/f400.go" "$tmp/size400/"
expect pass "file-size accepts 400 lines" scripts/check-file-size.sh 400 "$tmp/none.txt" "$tmp/size400"
mkdir -p "$tmp/size401"
mv "$tmp/size/f401.go" "$tmp/size401/"
echo "$tmp/size401/f401.go" > "$tmp/allow.txt"
expect pass "file-size honours the exception list" scripts/check-file-size.sh 400 "$tmp/allow.txt" "$tmp/size401"
expect fail "file-size rejects 401 lines in a directory" scripts/check-file-size.sh 400 "$tmp/none.txt" "$tmp/size401"

# check-coverage
cat > "$tmp/cover.out" <<'PROFILE'
mode: atomic
example.com/m/internal/gate/a.go:1.1,2.2 8 1
example.com/m/internal/gate/a.go:3.1,4.2 2 0
example.com/m/internal/sched/b.go:1.1,2.2 5 0
PROFILE
expect pass "coverage accepts 80% at threshold 80" scripts/check-coverage.sh "$tmp/cover.out" 80 internal/gate
expect fail "coverage rejects 80% at threshold 90" scripts/check-coverage.sh "$tmp/cover.out" 90 internal/gate
expect fail "coverage rejects an uncovered package" scripts/check-coverage.sh "$tmp/cover.out" 90 internal/sched
mkdir -p "$tmp/w/internal/store"
echo "package store" > "$tmp/w/internal/store/s.go"
expect pass "coverage skips a package without code" bash -c "cd '$tmp/w' && '$PWD/scripts/check-coverage.sh' '$tmp/cover.out' 90 internal/hook"
expect fail "coverage rejects a package with code but no tests" bash -c "cd '$tmp/w' && '$PWD/scripts/check-coverage.sh' '$tmp/cover.out' 90 internal/store"

# check-scenarios
sc=scripts/testdata/scenarios
expect pass "scenarios report exits 0 with missing IDs" scripts/check-scenarios.sh --report $sc/scenarios.md $sc/tests
expect fail "scenarios strict exits 1 with missing IDs" scripts/check-scenarios.sh --strict $sc/scenarios.md $sc/tests
expect pass "scenarios strict exits 0 when all IDs are covered" scripts/check-scenarios.sh --strict $sc/scenarios.md $sc/full
expect fail "scenarios report fails on an unknown ID" scripts/check-scenarios.sh --report $sc/scenarios.md $sc/unknown
expect fail "scenarios strict fails on an unknown ID" scripts/check-scenarios.sh --strict $sc/scenarios.md $sc/unknown
expect error "scenarios without a mode is a usage error" scripts/check-scenarios.sh $sc/scenarios.md
expect pass "scenarios report passes on the repo tree" scripts/check-scenarios.sh --report
expect error "scenarios rejects a missing directory" scripts/check-scenarios.sh --report $sc/scenarios.md $sc/nope
equals() { # equals <name> <want> <got>
  if [ "$2" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1: wanted '$2', got '$3'" >&2; failures=$((failures + 1)); fi
}
equals "scenarios counts IDs per section" "1. Alpha: 1 of 2 covered, missing: AA-2
2. Beta: 1 of 2 covered, missing: BB-10" "$(scripts/check-scenarios.sh --report $sc/scenarios.md $sc/tests 2>&1)"
equals "scenarios report lists every section" "$(grep -c '^## [0-9]*\. ' docs/dev/scenarios.md)" "$(scripts/check-scenarios.sh --report | grep -c '^[0-9]*\. ')"

[ "$failures" -eq 0 ]
