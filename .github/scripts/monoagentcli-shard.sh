#!/usr/bin/env bash
# Prints the cmd/monoagentcli test names that belong to one shard, as a
# regex alternation ("TestA|TestB|..."), for `go test -run "^(...)$"`.
#
# usage: monoagentcli-shard.sh SHARD COUNT [WEIGHTS_FILE] < test-names
#
# Test names come on stdin, one per line. Every name goes to exactly one of the
# COUNT shards (0..COUNT-1), so the union of the shards is the whole list.
# Names are placed heaviest first, each onto the shard with the least total
# weight so far (longest-processing-time-first). The weight of a name is its
# measured seconds in WEIGHTS_FILE (default: monoagentcli-test-weights.txt
# beside this script; lines "Name seconds", '#' lines ignored). A name the file
# does not list (a new test) weighs the file's median, so it still spreads out.
# The result depends only on the input names and the file, never on the order
# of the input, so every shard job computes the same partition.
set -euo pipefail

shard="${1:?shard}"
count="${2:?count}"
weights="${3:-$(dirname "$0")/monoagentcli-test-weights.txt}"

[ -r "$weights" ] || { echo "no weights file: $weights" >&2; exit 2; }

LC_ALL=C awk -v shard="$shard" -v count="$count" '
  FNR == NR {
    if ($0 ~ /^[[:space:]]*(#|$)/) next
    w[$1] = $2 + 0; ws[++nw] = $2 + 0
    next
  }
  $0 ~ /^(Test|Example)/ { names[++n] = $0 }
  END {
    if (n == 0) exit 3
    # median of the measured weights, for tests the file does not know
    for (i = 2; i <= nw; i++) { v = ws[i]; j = i - 1; while (j > 0 && ws[j] > v) { ws[j+1] = ws[j]; j-- } ws[j+1] = v }
    med = nw ? ws[int((nw + 1) / 2)] : 1
    for (i = 1; i <= n; i++) wt[i] = (names[i] in w) ? w[names[i]] : med
    # heaviest first, name as the tie-break (insertion sort over indexes)
    for (i = 1; i <= n; i++) idx[i] = i
    for (i = 2; i <= n; i++) {
      v = idx[i]; j = i - 1
      while (j > 0 && (wt[idx[j]] < wt[v] || (wt[idx[j]] == wt[v] && names[idx[j]] > names[v]))) { idx[j+1] = idx[j]; j-- }
      idx[j+1] = v
    }
    for (s = 0; s < count; s++) load[s] = 0
    for (i = 1; i <= n; i++) {
      b = 0
      for (s = 1; s < count; s++) if (load[s] < load[b]) b = s
      load[b] += wt[idx[i]]
      if (b == shard + 0) out[++m] = names[idx[i]]
    }
    for (i = 1; i <= m; i++) printf "%s%s", (i > 1 ? "|" : ""), out[i]
    printf "\n"
  }
' "$weights" -
