#!/usr/bin/env bash
# Prints the packages that belong to one default test shard (r0..r3), one import path per line,
# heaviest first (so the long ones start first under `go test -p`).
#
# usage: ci-rshard-packages.sh SHARD COUNT [WEIGHTS_FILE] < package-import-paths
#
# Packages come on stdin, one per line (the output of `go list`). Every package goes to exactly
# one of the COUNT shards (r0..r<COUNT-1>, or the bare index), so the union of the shards is the
# whole list. Packages are placed heaviest first, each onto the shard with the least total weight
# so far (longest-processing-time-first). The weight of a package is its measured seconds in
# WEIGHTS_FILE (default: ci-package-weights.txt beside this script; lines "path seconds", paths
# relative to the module, '#' lines ignored); a package the file does not list weighs 5. The
# result depends only on the input and the file, never on the order of the input, so every shard
# job computes the same partition.
set -euo pipefail

shard="${1:?shard}"
count="${2:?count}"
weights="${3:-$(dirname "$0")/ci-package-weights.txt}"
shard="${shard#r}"

[ -r "$weights" ] || { echo "no weights file: $weights" >&2; exit 2; }

LC_ALL=C awk -v shard="$shard" -v count="$count" '
  FNR == NR {
    if ($0 ~ /^[[:space:]]*(#|$)/) next
    w[$1] = $2 + 0
    next
  }
  NF { names[++n] = $1 }
  END {
    if (n == 0) exit 3
    for (i = 1; i <= n; i++) {
      p = names[i]; sub(/^github\.com\/monoes\/mono-agent\/?/, "", p)
      wt[i] = (p in w) ? w[p] : 5
    }
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
      if (b == shard + 0) print names[idx[i]]
    }
  }
' "$weights" -
