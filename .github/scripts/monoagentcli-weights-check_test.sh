#!/usr/bin/env bash
# Tests monoagentcli-weights-check.sh: up to the limit of unlisted tests passes, one more fails and
# names the lines to add, a duplicate or malformed weights line fails, empty input is refused.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
chk="$here/monoagentcli-weights-check.sh"
limit="$(sed -n 's/^MAX_MISSING=\([0-9]*\).*/\1/p' "$chk")"
d="$(mktemp -d)"
fail=0
ok() { echo "ok    $1"; }
bad() { echo "FAIL  $1"; fail=1; }

printf '# comment\nTestA 5\nTestB 2.5\nExampleC 1\n' >"$d/w.txt"
names() { # COUNT -> the listed tests plus COUNT unlisted ones
  printf '%s\n' TestA TestB ExampleC 'ok  github.com/x/y 0.1s'
  i=0; while [ "$i" -lt "$1" ]; do echo "TestNew$i"; i=$((i+1)); done
}

[ -n "$limit" ] || bad "could not read MAX_MISSING"
if names "$limit" | bash "$chk" "$d/w.txt" >/dev/null 2>&1; then ok "N unlisted tests pass"; else bad "N unlisted tests fail"; fi
if out="$(names $((limit+1)) | bash "$chk" "$d/w.txt" 2>&1)"; then bad "N+1 unlisted tests pass"; else
  if grep -qx 'TestNew0 0' <<<"$out"; then ok "N+1 unlisted tests fail and print the line to add"; else bad "failure does not print the lines to add"; fi
fi
if names 0 | bash "$chk" "$d/w.txt" >/dev/null 2>&1; then ok "all listed passes"; else bad "all listed fails"; fi
# the same unlisted name twice in the input counts once
if { names "$limit"; echo TestNew0; } | bash "$chk" "$d/w.txt" >/dev/null 2>&1; then ok "a repeated unlisted name counts once"; else bad "repeated name double-counted"; fi

printf 'TestA 5\nTestA 6\n' >"$d/dup.txt"
if names 0 | bash "$chk" "$d/dup.txt" >/dev/null 2>&1; then bad "duplicate entry accepted"; else ok "duplicate entry fails"; fi
for line in 'TestA fast' 'TestA' 'TestA 5 6' 'TestA -3' 'helper 5'; do
  printf '%s\n' "$line" >"$d/bad.txt"
  if names 0 | bash "$chk" "$d/bad.txt" >/dev/null 2>&1; then bad "malformed line accepted: $line"; else ok "malformed line fails: $line"; fi
done
if bash "$chk" "$d/w.txt" </dev/null >/dev/null 2>&1; then bad "empty input accepted"; else ok "empty input is refused"; fi
if printf 'ok pkg 1s\n' | bash "$chk" "$d/w.txt" >/dev/null 2>&1; then bad "input without test names accepted"; else ok "input without test names is refused"; fi
if names 0 | bash "$chk" "$d/none.txt" >/dev/null 2>&1; then bad "missing weights file accepted"; else ok "missing weights file is refused"; fi
if names 0 | bash "$chk" "$here/monoagentcli-test-weights.txt" >/dev/null 2>&1; then ok "the real weights file is well-formed"; else bad "the real weights file is malformed"; fi
exit $fail
