#!/usr/bin/env bash
# Tests ci-rshard-packages.sh: the shards partition the packages (each in exactly one shard, none
# lost, none doubled), the result does not depend on the input order, an unlisted package still
# lands somewhere, the heavy packages are spread apart, and the shipped weights file is well formed
# and splits its own packages into shards within 10% of each other (the point of the script).
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
sh="$here/ci-rshard-packages.sh"
d="$(mktemp -d)"
fail=0
ok() { echo "ok    $1"; }
bad() { echo "FAIL  $1"; fail=1; }
M=github.com/monoes/mono-agent

cat >"$d/w.txt" <<'W'
# comment
internal/big 100
internal/mid1 30
internal/mid2 30
internal/small1 5
W
printf '%s\n' $M/internal/small1 $M/internal/big $M/internal/new $M/internal/mid2 $M/internal/mid1 $M >"$d/pk.txt"
sort -r "$d/pk.txt" >"$d/pk-rev.txt"
for s in 0 1 2; do
  bash "$sh" "r$s" 3 "$d/w.txt" <"$d/pk.txt" | sort >"$d/as$s"
  bash "$sh" "$s" 3 "$d/w.txt" <"$d/pk-rev.txt" | sort >"$d/bs$s"
done
cat "$d"/as0 "$d"/as1 "$d"/as2 | sort >"$d/all"
sort "$d/pk.txt" >"$d/want"
if cmp -s "$d/all" "$d/want"; then ok "every package (listed or new) is in exactly one shard"; else bad "shards do not partition the packages"; fi
if cmp -s "$d/as0" "$d/bs0" && cmp -s "$d/as1" "$d/bs1" && cmp -s "$d/as2" "$d/bs2"; then ok "the split does not depend on input order"; else bad "the split depends on input order"; fi
apart=1
for s in 0 1 2; do
  if grep -qx $M/internal/big "$d/as$s" && { grep -qx $M/internal/mid1 "$d/as$s" || grep -qx $M/internal/mid2 "$d/as$s"; }; then apart=0; fi
done
if [ "$apart" = 1 ]; then ok "the heaviest package does not share a shard with the mid-weight ones"; else bad "heavy packages share a shard"; fi
if [ "$(bash "$sh" r0 3 "$d/w.txt" <"$d/pk.txt" | head -1)" = "$M/internal/big" ]; then ok "the heaviest package comes first in its shard"; else bad "heaviest package is not listed first"; fi
if bash "$sh" r0 3 "$d/w.txt" </dev/null >/dev/null 2>&1; then bad "empty input must fail"; else ok "empty input fails"; fi

real="$here/ci-package-weights.txt"
if grep -vE '^[[:space:]]*(#|$)' "$real" | awk 'NF != 2 || $1 !~ /^[A-Za-z0-9_.\/-]+$/ || $2 !~ /^[0-9]+$/ || ($1 in s) {bad=1} {s[$1]=1} END{exit bad}'; then ok "the weights file is well formed (path, whole seconds, no duplicate)"; else bad "the weights file has a malformed or duplicate line"; fi
grep -vE '^[[:space:]]*(#|$)' "$real" | awk -v m="$M" '{print m "/" $1}' >"$d/real-pk.txt"
for s in 0 1 2 3; do
  bash "$sh" "r$s" 4 <"$d/real-pk.txt" | sed "s|^$M/||" | awk -v f="$real" 'BEGIN{while((getline l<f)>0){split(l,a," "); if(l!~/^#/&&a[2]!="")w[a[1]]=a[2]}} {t+=w[$1]} END{print t}' >"$d/load$s"
done
if cat "$d"/load0 "$d"/load1 "$d"/load2 "$d"/load3 | awk '{if(min==""||$1<min)min=$1; if($1>max)max=$1} END{exit !(max <= min*1.10)}'; then ok "the shipped weights split into shards within 10% of each other"; else bad "shards unbalanced: $(cat "$d"/load0 "$d"/load1 "$d"/load2 "$d"/load3 | tr '\n' ' ')"; fi
exit "$fail"
