#!/usr/bin/env bash
# Guards monoagentcli-test-weights.txt, which monoagentcli-shard.sh uses to balance the test shards.
#
# usage: monoagentcli-weights-check.sh [WEIGHTS_FILE] < test-names
#
# Test names come on stdin (the output of `go test ./cmd/monoagentcli -list '^(Test|Example)'`;
# lines that are not Test*/Example* are ignored). Fails (exit 1) when
#   - more than MAX_MISSING tests have no entry in the weights file: a test missing from the file
#     weighs the median, so new heavy tests would silently unbalance a shard until it times out;
#     the failure prints the lines to add ("Name 0": put the measured seconds instead of 0), or
#   - a weights line is malformed (not "Name seconds") or lists a name twice.
# Exits 3 on empty input, 2 when the weights file cannot be read.
set -euo pipefail

# How many unlisted tests are tolerated, so a PR that adds a few cheap tests does not have to touch
# the file. More than this means a batch of new tests: add them with their measured seconds.
MAX_MISSING=10

weights="${1:-$(dirname "$0")/monoagentcli-test-weights.txt}"
[ -r "$weights" ] || { echo "no weights file: $weights" >&2; exit 2; }

LC_ALL=C awk -v max="$MAX_MISSING" '
  FNR == NR {
    if ($0 ~ /^[[:space:]]*(#|$)/) next
    if (NF != 2 || $1 !~ /^(Test|Example)/ || $2 !~ /^[0-9]+(\.[0-9]+)?$/) {
      printf "malformed weights line %d: %s (want \"Name seconds\")\n", FNR, $0; bad = 1; next
    }
    if ($1 in w) { printf "duplicate weights entry: %s\n", $1; bad = 1 }
    w[$1] = 1
    next
  }
  $0 ~ /^(Test|Example)/ { n++; if (!($0 in w) && !($0 in seen)) miss[++m] = $0; seen[$0] = 1 }
  END {
    if (n == 0) { print "no test names on stdin" > "/dev/stderr"; exit 3 }
    if (m > max) {
      printf "%d tests have no weight (more than %d); add these lines to the weights file with their measured seconds:\n", m, max
      for (i = 1; i <= m; i++) printf "%s 0\n", miss[i]
      bad = 1
    }
    exit bad
  }
' "$weights" -
