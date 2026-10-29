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
  *"/runs/failed/"*) f=failed ;;
  *"/runs/empty/"*) f=empty ;;
  *) exit 1 ;;
esac
cat "$FIXTURES/$f.jsonl"
GH
chmod +x "$d/bin/gh"
export FIXTURES="$d"
ok='{"name":"Changed paths","conclusion":"success","steps":[]}'
shard='{"name":"Test shard (default 0)","conclusion":"success","steps":[{"name":"Set up job","conclusion":"success"},{"name":"Test","conclusion":"success"}]}'
nshard='{"name":"Test shard (nosocial 0)","conclusion":"success","steps":[{"name":"Test","conclusion":"success"}]}'
printf '%s\n' "$ok" "$shard" "$nshard" '{"name":"Account package (macos-latest)","conclusion":"success","steps":[]}' >"$d/full.jsonl"
# docs-only style trim: shards and others skipped
printf '%s\n' "$ok" '{"name":"Test shard (default 0)","conclusion":"skipped","steps":[]}' >"$d/trimmed.jsonl"
# nosocial trimmed: the cell's job is green but its Test step was skipped
printf '%s\n' "$ok" "$shard" '{"name":"Test shard (nosocial 0)","conclusion":"success","steps":[{"name":"Test","conclusion":"skipped"}]}' >"$d/cell.jsonl"
printf '%s\n' "$ok" "$shard" '{"name":"Wails GUI module","conclusion":"failure","steps":[]}' >"$d/failed.jsonl"
: >"$d/empty.jsonl"

fail=0
t() { # name run want-exit
  local got=1
  PATH="$d/bin:$PATH" bash "$here/ci-run-is-full.sh" o/r "$2" && got=0
  if [ "$got" = "$3" ]; then echo "ok    $1"; else echo "FAIL  $1 (exit $got, want $3)"; fail=1; fi
}
t "full run is proof" full 0
t "trimmed run (skipped jobs) is not" trimmed 1
t "skipped nosocial Test step is not" cell 1
t "run with a failed job is not" failed 1
t "no jobs returned is not" empty 1
t "gh failing is not (fail closed)" nosuch 1
exit $fail
