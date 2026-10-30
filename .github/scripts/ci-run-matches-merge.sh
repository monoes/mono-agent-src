#!/usr/bin/env bash
# usage: ci-run-matches-merge.sh REPO RUN_ID BRANCH P2 SUBJECT SHA
# Exit 0 only when that CI run is a pull_request run of THE pull request that
# GitHub says produced the merge commit SHA now being released, and prints that
# PR's number. The merged PR comes from the API (repos/REPO/commits/SHA/pulls),
# never from the subject, which the merger can edit: exactly one PR must be
# closed and merged, have merge_commit_sha == SHA, base.ref == BRANCH and
# head.sha == P2 (the merge commit's second parent). If SUBJECT has the form
# "Merge pull request #N ..." then N must equal that PR's number. The run must be
# event pull_request, head_sha P2, and list that same number in pull_requests[]
# with head.sha P2, base.ref BRANCH and base.repo.id this repository. Two PRs can
# share a head and target different branches; the run of the other one merged a
# different base tree and proves nothing here. A missing field, a failed or
# malformed API call, zero or several candidate PRs, a number mismatch or an empty
# argument exits 1 (fail closed: the release then runs its own tests). A run from
# a fork PR lists no pull_requests[] and is therefore never accepted.
set -uo pipefail
repo="$1" id="$2" branch="$3" p2="$4" subject="$5" sha="${6:-}"
[ -n "$p2" ] && [ -n "$sha" ] || exit 1
cands="$(gh api "repos/$repo/commits/$sha/pulls" 2>/dev/null | jq -ce \
  --arg sha "$sha" --arg branch "$branch" --arg p2 "$p2" '
  [.[] | select(.state == "closed" and .merged_at != null and .merge_commit_sha == $sha
                and .base.ref == $branch and .head.sha == $p2) | .number]' 2>/dev/null)" || exit 1
num="$(jq -ce 'if length == 1 then .[0] else empty end' <<<"$cands")" || exit 1
[[ "$num" =~ ^[0-9]+$ ]] || exit 1
if [[ "$subject" =~ ^Merge\ pull\ request\ \#([0-9]+)([[:space:]]|$) ]]; then
  [ "${BASH_REMATCH[1]}" = "$num" ] || exit 1
fi
repo_id="$(gh api "repos/$repo" --jq .id 2>/dev/null)" || exit 1
[[ "$repo_id" =~ ^[0-9]+$ ]] || exit 1
gh api "repos/$repo/actions/runs/$id" 2>/dev/null | jq -e \
  --arg p2 "$p2" --arg branch "$branch" --argjson num "$num" --argjson rid "$repo_id" '
  .event == "pull_request" and .head_sha == $p2
  and any(.pull_requests[]?;
        .number == $num and .head.sha == $p2
        and .base.ref == $branch and .base.repo.id == $rid)
' >/dev/null || exit 1
echo "$num"
