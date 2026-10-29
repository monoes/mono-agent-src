#!/usr/bin/env bash
# usage: ci-run-is-full.sh REPO RUN_ID
# Exit 0 only when that CI run was a FULL run: every job concluded success (none
# skipped, so no path rule trimmed anything) and every "Test shard" job really
# ran its Test step (the nosocial cells are skipped step by step, which leaves
# the job green). Anything else -- a trimmed run, no shard jobs, a failed or
# unreadable API call -- exits 1 (fail closed: the release then runs its own tests).
set -uo pipefail
repo="$1" id="$2"
jobs="$(gh api --paginate "repos/$repo/actions/runs/$id/jobs?per_page=100" --jq '.jobs[]' 2>/dev/null)" || exit 1
[ -n "$jobs" ] || exit 1
printf '%s\n' "$jobs" | jq -es '
  length > 0
  and all(.[]; .conclusion == "success")
  and ([.[] | select(.name | startswith("Test shard"))] | length) > 0
  and all(.[] | select(.name | startswith("Test shard"));
          any(.steps[]?; .name == "Test" and .conclusion == "success"))
' >/dev/null
