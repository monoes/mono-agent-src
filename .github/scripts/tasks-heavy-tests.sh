#!/usr/bin/env bash
# Prints the internal/tasks heavy property test names as a regex alternation
# ("TestA|TestB"), for `go test -run "^(...)$"` (shards tp1, tp2: pass the shard
# as the argument to get its tests) and `go test -skip "^(...)$"` (every shard
# that also builds internal/tasks: no argument, all of them).
set -euo pipefail
f="$(dirname "$0")/tasks-heavy-tests.txt"
[ -r "$f" ] || { echo "no heavy list: $f" >&2; exit 2; }
out="$(awk -v s="${1:-}" '$1 ~ /^Test[A-Za-z0-9_]*$/ && NF == 2 && (s == "" || $2 == s) {print $1}' "$f" | paste -sd'|' -)"
[ -n "$out" ] || { echo "empty heavy list" >&2; exit 3; }
printf '%s\n' "$out"
