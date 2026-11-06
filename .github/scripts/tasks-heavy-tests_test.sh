#!/usr/bin/env bash
# Guards the internal/tasks heavy-test split (tasks-heavy-tests.txt): every listed
# name exists exactly once in the repo's tests (so a package-wide -skip cannot hide
# a same-named test elsewhere), the heavy pattern selects exactly the listed tests (-run and -skip take the same
# anchored pattern, so the two sides partition the package), and ci.yml wires the skip into the
# shards that build internal/tasks and the -run into the tp shard.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
root="$here/../.."
cd "$root"
fail=0
ok() { echo "ok    $1"; }
bad() { echo "FAIL  $1"; fail=1; }

heavy="$(bash "$here/tasks-heavy-tests.sh")"
all="$(go test ./internal/tasks -list '^(Test|Example)' | grep -E '^(Test|Example)' | sort)"
run="$(go test ./internal/tasks -list "^($heavy)\$" | grep -E '^(Test|Example)' | sort)"
want="$(printf '%s\n' "${heavy//|/$'\n'}" | sort)"

[ "$run" = "$want" ] && ok "the heavy pattern selects exactly the listed tests" || bad "pattern selected: $(echo $run) (listed: $(echo $want))"
rest="$(comm -23 <(echo "$all") <(echo "$want"))"
[ "$(echo "$rest" | wc -l)" -eq "$(( $(echo "$all" | wc -l) - $(echo "$want" | wc -l) ))" ] && ok "every listed test is in internal/tasks" || bad "a listed test is not in internal/tasks"

for n in ${heavy//|/ }; do
  c="$(grep -rl --include='*_test.go' --exclude-dir=node_modules --exclude-dir=.git -E "^func $n\(" . | wc -l)"
  [ "$c" -eq 1 ] && ok "$n is defined once in the repo" || bad "$n is defined in $c files"
done

yml="$here/../workflows/ci.yml"
grep -qF 'tasks-heavy-tests.sh' "$yml" && ok "ci.yml uses the heavy list" || bad "ci.yml does not use the heavy list"
[ "$(grep -cE 'go test .*-skip "\^\(\$heavy\)\\\$"' "$yml")" -ge 3 ] && ok "ci.yml skips the heavy tests in nosocial rest, rest and r1-r3" || bad "ci.yml skip lines missing"
grep -qE 'go test .*\./internal/tasks -race -run "\^\(\$\(bash \.github/scripts/tasks-heavy-tests\.sh "\$SHARD"\)\)\\\$"' "$yml" && ok "ci.yml runs each tp shard's own heavy tests" || bad "ci.yml tp run line missing"
t1="$(bash "$here/tasks-heavy-tests.sh" tp1)"; t2="$(bash "$here/tasks-heavy-tests.sh" tp2)"
u="$(printf '%s\n' "${t1//|/$'\n'}" "${t2//|/$'\n'}" | sort)"
[ "$u" = "$want" ] && ok "tp1 and tp2 together run every listed test exactly once" || bad "tp1+tp2 differ from the list: $(echo $u)"
for sh in tp1 tp2; do grep -qxF "Test shard (default $sh)" "$here/ci-expected-shards.txt" && ok "$sh is on the release proof list" || bad "$sh missing from ci-expected-shards.txt"; done
exit $fail
