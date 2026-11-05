#!/usr/bin/env bash
# Prints the internal/tasks heavy property test names as a regex alternation
# ("TestA|TestB"), for `go test -run "^(...)$"` (shard tp) and
# `go test -skip "^(...)$"` (every shard that also builds internal/tasks).
set -euo pipefail
f="$(dirname "$0")/tasks-heavy-tests.txt"
[ -r "$f" ] || { echo "no heavy list: $f" >&2; exit 2; }
out="$(grep -E '^Test[A-Za-z0-9_]*$' "$f" | paste -sd'|' -)"
[ -n "$out" ] || { echo "empty heavy list" >&2; exit 3; }
printf '%s\n' "$out"
