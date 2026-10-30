#!/usr/bin/env bash
# Tests ci-run-matches-merge.sh with a fake `gh` on PATH returning canned run payloads.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
d="$(mktemp -d)"
trap 'find "$d" -mindepth 1 -delete; rmdir "$d"' EXIT
mkdir "$d/bin"
cat >"$d/bin/gh" <<'GH'
#!/usr/bin/env bash
case "$*" in
  *"repos/o/r/actions/runs/"*) a="$*"; id="${a#*actions/runs/}"; cat "$FIXTURES/${id%% *}.json" ;;
  "api repos/o/r --jq .id") echo 100 ;;
  *) exit 1 ;;
esac
GH
chmod +x "$d/bin/gh"
export FIXTURES="$d"
P2=bbbb
# run ID EVENT HEAD_SHA PULL_REQUESTS_JSON
run() { printf '{"id":1,"event":"%s","head_sha":"%s","pull_requests":%s}\n' "$2" "$3" "$4" >"$d/$1.json"; }
pr() { printf '[{"number":%s,"head":{"sha":"%s"},"base":{"ref":"%s","sha":"aaaa","repo":{"id":%s}}}]' "$1" "$2" "$3" "$4"; }
run good pull_request $P2 "$(pr 7 $P2 master 100)"
run otherbase pull_request $P2 "$(pr 7 $P2 release/1 100)"
run otherrepo pull_request $P2 "$(pr 7 $P2 master 999)"
run othernum pull_request $P2 "$(pr 8 $P2 master 100)"
run otherhead pull_request $P2 "$(pr 7 cccc master 100)"
run otherrun pull_request cccc "$(pr 7 $P2 master 100)"
run push push $P2 "$(pr 7 $P2 master 100)"
run emptyprs pull_request $P2 '[]'
printf '{"id":1,"event":"pull_request","head_sha":"%s"}\n' $P2 >"$d/nokey.json"
run twoprs pull_request $P2 "[{\"number\":8,\"head\":{\"sha\":\"$P2\"},\"base\":{\"ref\":\"release/1\",\"repo\":{\"id\":100}}},{\"number\":7,\"head\":{\"sha\":\"$P2\"},\"base\":{\"ref\":\"master\",\"repo\":{\"id\":100}}}]"
printf '{"id":1,"event":"pull_request","head_sha":"%s","pull_requests":[{"number":7,"head":{"sha":"%s"},"base":{"ref":"master"}}]}\n' $P2 $P2 >"$d/norepo.json"

fail=0
t() { # name run subject want
  local got=1
  PATH="$d/bin:$PATH" bash "$here/ci-run-matches-merge.sh" o/r "$2" master $P2 "$3" && got=0
  if [ "$got" = "$4" ]; then echo "ok    $1"; else echo "FAIL  $1 (exit $got, want $4)"; fail=1; fi
}
S='Merge pull request #7 from fork/branch'
t "the right run is accepted" good "$S" 0
t "the same head in a PR with another base branch is rejected" otherbase "$S" 1
t "a PR with another base repository is rejected" otherrepo "$S" 1
t "a PR number differing from the merge subject is rejected" othernum "$S" 1
t "a PR whose head is not the second parent is rejected" otherhead "$S" 1
t "a run whose head_sha is not the second parent is rejected" otherrun "$S" 1
t "a non pull_request event is rejected" push "$S" 1
t "empty pull_requests is rejected" emptyprs "$S" 1
t "missing pull_requests is rejected" nokey "$S" 1
t "missing base.repo.id is rejected" norepo "$S" 1
t "the matching entry among several is accepted" twoprs "$S" 0
t "a subject that is not 'Merge pull request #N' is rejected" good 'Merge branch x into master' 1
t "gh failing is rejected" nosuch "$S" 1
exit $fail
