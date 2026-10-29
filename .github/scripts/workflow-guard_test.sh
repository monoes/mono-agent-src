#!/usr/bin/env bash
# Static guard for the public repository: no workflow may use pull_request_target
# or workflow_run, name a self-hosted runner literally, or route with anything
# but the sanctioned runs-on expression. Comment lines are ignored.
# It cannot stop a fork PR that edits the workflow AND this test; see the
# SECURITY note at the top of ci.yml (fork approval setting + reading the diff).
set -euo pipefail
cd "$(dirname "$0")/../.."
fail=0
routed="runs-on: \${{ fromJSON(((github.event_name != 'pull_request' || github.event.pull_request.head.repo.full_name == github.repository) && vars.CI_RUNNER) || '\"ubuntu-latest\"') }}"
for f in .github/workflows/*.yml .github/workflows/*.yaml; do
  [ -e "$f" ] || continue
  code="$(sed -E 's/^[[:space:]]*#.*$//' "$f")"
  if printf '%s\n' "$code" | grep -nE 'pull_request_target|^[[:space:]]*workflow_run:'; then
    echo "FAIL  $f uses pull_request_target or workflow_run"
    fail=1
  fi
  if printf '%s\n' "$code" | grep -nE 'self-hosted'; then
    echo "FAIL  $f names self-hosted literally (use vars.CI_RUNNER via the sanctioned expression)"
    fail=1
  fi
  # every line that mentions CI_RUNNER must be exactly the sanctioned runs-on line
  while IFS= read -r line; do
    t="${line#"${line%%[![:space:]]*}"}"
    if [ "$t" != "$routed" ]; then
      echo "FAIL  $f: unsanctioned routing: $t"
      fail=1
    fi
  done < <(printf '%s\n' "$code" | grep 'CI_RUNNER' || true)
done
[ "$fail" -eq 0 ] && echo "ok    workflows: no pull_request_target/workflow_run/self-hosted literal, routing sanctioned"
exit $fail
