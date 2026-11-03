#!/usr/bin/env bash
# Tests monoagentcli-shard.sh: the shards partition the test names (each name in
# exactly one shard, none lost, none doubled), the result does not depend on the
# input order, a name missing from the weights file still lands somewhere, and
# the heavy tests are spread apart (the point of the script).
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
sh="$here/monoagentcli-shard.sh"
d="$(mktemp -d)"
fail=0
ok() { echo "ok    $1"; }
bad() { echo "FAIL  $1"; fail=1; }

cat >"$d/w.txt" <<'W'
# comment
TestBig 100
TestMid1 30
TestMid2 30
TestSmall1 5
TestSmall2 5
TestSmall3 5
TestSmall4 5
W
printf '%s\n' TestSmall4 TestBig TestNew TestSmall1 TestMid2 TestSmall3 TestMid1 TestSmall2 ExampleNew >"$d/names.txt"
sort -r "$d/names.txt" >"$d/names-rev.txt"
for s in 0 1 2; do
  bash "$sh" "$s" 3 "$d/w.txt" <"$d/names.txt" | tr '|' '\n' | sort >"$d/as$s"
  bash "$sh" "$s" 3 "$d/w.txt" <"$d/names-rev.txt" | tr '|' '\n' | sort >"$d/bs$s"
done
cat "$d"/as0 "$d"/as1 "$d"/as2 | sort >"$d/all"
sort "$d/names.txt" >"$d/want"
if cmp -s "$d/all" "$d/want"; then ok "every name (listed or new) is in exactly one shard"; else bad "shards do not partition the names"; fi
if cmp -s "$d/as0" "$d/bs0" && cmp -s "$d/as1" "$d/bs1" && cmp -s "$d/as2" "$d/bs2"; then ok "the split does not depend on input order"; else bad "the split depends on input order"; fi
apart=1
for s in 0 1 2; do
  if grep -qx TestBig "$d/as$s" && { grep -qx TestMid1 "$d/as$s" || grep -qx TestMid2 "$d/as$s"; }; then apart=0; fi
done
if [ "$apart" = 1 ]; then ok "the heaviest test does not share a shard with the mid-weight ones"; else bad "heavy tests share a shard"; fi

real="$here/monoagentcli-test-weights.txt"
if grep -vE '^(#|$)' "$real" | awk '$1 !~ /^(Test|Example)/ || $2 !~ /^[0-9]+$/ {bad=1} END {exit bad}'; then ok "weights file lines are 'Name seconds'"; else bad "malformed weights file line"; fi
if bash "$sh" 0 4 "$real" </dev/null >/dev/null 2>&1; then bad "empty input accepted"; else ok "empty input is refused"; fi
exit $fail
