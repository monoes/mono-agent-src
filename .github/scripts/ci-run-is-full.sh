#!/usr/bin/env bash
# usage: ci-run-is-full.sh REPO RUN_ID [EXPECTED_FILE]
# Exit 0 only when that CI run is proof that the root module's tests passed:
# every job name listed in EXPECTED_FILE (default: ci-expected-shards.txt beside
# this script) is present with conclusion success, and every listed "Test shard"
# job really ran its Test step (the nosocial cells are skipped step by step,
# which leaves a job green). Jobs not on the list (extension, account, nosocial,
# wails, informational) are ignored, so a typical pull request that the path
# rules trimmed still counts. A missing name (a trimmed matrix, a skipped job),
# an empty list, a failed or unreadable API call -- exits 1 (fail closed: the
# release then runs its own tests).
set -uo pipefail
repo="$1" id="$2"
list="${3:-$(dirname "$0")/ci-expected-shards.txt}"
names="$(grep -vE '^[[:space:]]*(#|$)' "$list" 2>/dev/null | jq -R . | jq -s .)" || exit 1
[ -n "$names" ] || exit 1
jobs="$(gh api --paginate "repos/$repo/actions/runs/$id/jobs?per_page=100" --jq '.jobs[]' 2>/dev/null)" || exit 1
[ -n "$jobs" ] || exit 1
printf '%s\n' "$jobs" | jq -es --argjson want "$names" '
  . as $jobs
  | ($want | length) > 0
  and all($want[]; . as $n
    | any($jobs[]; .name == $n and .conclusion == "success"
        and (if ($n | startswith("Test shard"))
             then any(.steps[]?; .name == "Test" and .conclusion == "success")
             else true end)))
' >/dev/null
