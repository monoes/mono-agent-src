#!/usr/bin/env bash
# Tests ci-run-is-full.sh with a fake `gh` on PATH returning canned job lists.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
d="$(mktemp -d)"
trap 'find "$d" -mindepth 1 -delete; rmdir "$d"' EXIT
mkdir "$d/bin"
cat >"$d/bin/gh" <<'GH'
#!/usr/bin/env bash
# gh api --paginate repos/R/actions/runs/ID/jobs?per_page=100 --jq .jobs[]
case "$*" in
  *"/runs/full/"*) f=full ;;
  *"/runs/trimmed/"*) f=trimmed ;;
  *"/runs/cell/"*) f=cell ;;
  *"/runs/typical/"*) f=typical ;;
  *"/runs/dropped/"*) f=dropped ;;
  *"/runs/one/"*) f=one ;;
  *"/runs/failed/"*) f=failed ;;
  *"/runs/empty/"*) f=empty ;;
  *) exit 1 ;;
esac
cat "$FIXTURES/$f.jsonl"
GH
chmod +x "$d/bin/gh"
export FIXTURES="$d"
list="$here/ci-expected-shards.txt"
step='"steps":[{"name":"Set up job","conclusion":"success"},{"name":"Test","conclusion":"success"}]'
job() { printf '{"name":"%s","conclusion":"%s",%s}\n' "$1" "$2" "${3:-$step}"; }
# every expected name, successful (built from the checked-in list)
all_expected() { grep -vE '^[[:space:]]*(#|$)' "$list" | while IFS= read -r n; do job "$n" success; done; }
all_expected >"$d/full.jsonl"
# a typical trimmed PR: everything else skipped or green, still full
{ all_expected; job "Changed paths" success '"steps":[]'; job "Chrome extension" skipped '"steps":[]'
  job "Account gate smoke (devaccount)" skipped '"steps":[]'; job "Wails GUI module" skipped '"steps":[]'
  job "Test shard (nosocial 0)" success '"steps":[{"name":"Test","conclusion":"skipped"}]'
  job "Build & test (nosocial)" success '"steps":[]'; } >"$d/typical.jsonl"
# a PR that dropped a matrix cell: everything it ran is green
all_expected | grep -v 'default r3' >"$d/dropped.jsonl"
# a PR that dropped the whole matrix but kept one green shard (the old check accepted this)
{ job "Build & test (default)" success '"steps":[]'; job "Test shard (default 0)" success; } >"$d/one.jsonl"
# docs-only trim: shards and aggregator skipped
{ job "Changed paths" success '"steps":[]'; job "Test shard (default 0)" skipped '"steps":[]'; } >"$d/trimmed.jsonl"
# a listed shard whose Test step was skipped
all_expected | sed 's/"name":"Test shard (default 1)","conclusion":"success","steps":\[{"name":"Set up job","conclusion":"success"},{"name":"Test","conclusion":"success"}\]/"name":"Test shard (default 1)","conclusion":"success","steps":[{"name":"Test","conclusion":"skipped"}]/' >"$d/cell.jsonl"
# a listed job that failed
all_expected | sed 's/"name":"Build & test (default)","conclusion":"success"/"name":"Build \& test (default)","conclusion":"failure"/' >"$d/failed.jsonl"
: >"$d/empty.jsonl"

fail=0
t() { # name run want-exit
  local got=1
  PATH="$d/bin:$PATH" bash "$here/ci-run-is-full.sh" o/r "$2" && got=0
  if [ "$got" = "$3" ]; then echo "ok    $1"; else echo "FAIL  $1 (exit $got, want $3)"; fail=1; fi
}
t "full run is proof" full 0
t "typical trimmed PR (other jobs skipped) is proof" typical 0
t "PR that dropped a matrix cell is not" dropped 1
t "one green shard is not (partial matrix)" one 1
t "docs-only trim (shards skipped) is not" trimmed 1
t "skipped Test step of a listed shard is not" cell 1
t "listed job that failed is not" failed 1
t "no jobs returned is not" empty 1
t "gh failing is not (fail closed)" nosuch 1
# the expected list a PR cannot shrink: an explicit file wins, a missing or empty one fails closed
printf 'Build & test (default)\n' >"$d/small.txt"
PATH="$d/bin:$PATH" bash "$here/ci-run-is-full.sh" o/r one "$d/small.txt" && echo "ok    explicit expected file is honoured" || { echo "FAIL  explicit expected file"; fail=1; }
: >"$d/none.txt"
PATH="$d/bin:$PATH" bash "$here/ci-run-is-full.sh" o/r full "$d/none.txt" && { echo "FAIL  empty expected list accepted"; fail=1; } || echo "ok    empty expected list is not proof"
PATH="$d/bin:$PATH" bash "$here/ci-run-is-full.sh" o/r full "$d/missing.txt" && { echo "FAIL  missing expected list accepted"; fail=1; } || echo "ok    missing expected list is not proof"

# The list must name exactly the default-tag cells of the matrix in ci.yml.
if python3 - "$here/../workflows/ci.yml" "$list" <<'PY'
import sys, yaml
wf = yaml.safe_load(open(sys.argv[1]))
job = wf["jobs"]["test-shards"]
m = job["strategy"]["matrix"]
cells = [{"tags": t, "shard": str(s)} for t in m["tags"] for s in m["shard"]]
for ex in m.get("exclude", []):
    cells = [c for c in cells if not all(str(c.get(k)) == str(v) for k, v in ex.items())]
for inc in m.get("include", []):
    cells.append({k: str(v) for k, v in inc.items()})
tmpl = job["name"]
def name(c):
    return tmpl.replace("${{ matrix.tags }}", c["tags"]).replace("${{ matrix.shard }}", c["shard"])
want = {name(c) for c in cells if c["tags"] == "default"}
want.add(wf["jobs"]["test"]["name"])
# internal/agentbox needs a real host root and nested bwrap, so it runs in its own hosted job, not in a shard
want.add(wf["jobs"]["agentbox-test"]["name"])
got = {l.strip() for l in open(sys.argv[2]) if l.strip() and not l.lstrip().startswith("#")}
if want != got:
    print("missing from the list:", sorted(want - got)); print("not in ci.yml:", sorted(got - want)); sys.exit(1)
PY
then echo "ok    ci-expected-shards.txt matches the ci.yml matrix"; else echo "FAIL  ci-expected-shards.txt differs from the ci.yml matrix"; fail=1; fi

# release.yml: the first query is push-only, and the proof comes from the base commit's copies.
rel="$here/../workflows/release.yml"
if grep -qF 'runs "$SHA" '"'"'event=push&status=success'"'"'' "$rel"; then echo "ok    release guard's first query is event=push"; else echo "FAIL  release guard's first query lacks event=push"; fail=1; fi
if grep -qF 'git show "$p1:.github/scripts/ci-expected-shards.txt"' "$rel" && grep -qF 'git show "$p1:.github/scripts/ci-run-is-full.sh"' "$rel"; then echo "ok    release guard reads the list and the check from the base commit"; else echo "FAIL  release guard does not read the list/check from the base commit"; fail=1; fi
if grep -qF 'git show "$p1:.github/scripts/ci-run-matches-merge.sh"' "$rel" && grep -qF 'bash "$mch" "$REPO" "$id" "$BRANCH" "$p2" "$subject"' "$rel"; then echo "ok    release guard checks the PR run's identity and base with the base commit's script"; else echo "FAIL  release guard does not check the PR run's identity and base"; fail=1; fi
exit $fail
