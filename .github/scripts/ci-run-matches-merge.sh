#!/usr/bin/env bash
# usage: ci-run-matches-merge.sh REPO RUN_ID BRANCH P2 SUBJECT
# Exit 0 only when that CI run is a pull_request run of THE pull request that
# produced the merge commit now being released: event pull_request, head_sha P2
# (the merge commit's second parent), and one of its pull_requests[] entries is
# number N from the merge subject "Merge pull request #N ...", has head.sha P2,
# targets BRANCH (the branch being released) in THIS repository (base.repo.id).
# Two PRs can share a head and target different branches; the run of the other
# one merged a different base tree and proves nothing here. Any missing field,
# a subject of another form, or a failed API call exits 1 (fail closed: the
# release then runs its own tests). A run from a fork PR lists no
# pull_requests[] and is therefore never accepted.
set -uo pipefail
repo="$1" id="$2" branch="$3" p2="$4" subject="$5"
[[ "$subject" =~ ^Merge\ pull\ request\ \#([0-9]+)([[:space:]]|$) ]] || exit 1
num="${BASH_REMATCH[1]}"
repo_id="$(gh api "repos/$repo" --jq .id 2>/dev/null)" || exit 1
[[ "$repo_id" =~ ^[0-9]+$ ]] || exit 1
gh api "repos/$repo/actions/runs/$id" 2>/dev/null | jq -e \
  --arg p2 "$p2" --arg branch "$branch" --argjson num "$num" --argjson rid "$repo_id" '
  .event == "pull_request" and .head_sha == $p2
  and any(.pull_requests[]?;
        .number == $num and .head.sha == $p2
        and .base.ref == $branch and .base.repo.id == $rid)
' >/dev/null
