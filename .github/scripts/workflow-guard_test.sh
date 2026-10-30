#!/usr/bin/env bash
# Static guard for the public repository, as an ALLOWLIST over the parsed YAML:
#  - no workflow may trigger on pull_request_target or workflow_run, in any form
#    (string, list or mapping under `on:`);
#  - every job's runs-on must be exactly the sanctioned routing expression, or
#    one of ubuntu-latest / windows-latest / macos-latest, or ${{ matrix.os }}
#    where every matrix os value is one of those hosted labels.
# Anything else (a custom label, a runner group, another variable, a list such as
# [self-hosted]) fails. It also proves itself: each mutation fixture below must
# be REJECTED. Comments are not code, so the YAML parser ignores them.
# It cannot stop a fork PR that edits the workflow AND this test; see the
# SECURITY note at the top of ci.yml (fork approval setting + reading the diff).
set -euo pipefail
cd "$(dirname "$0")/../.."

check() { # DIR -> prints FAIL lines, exit 1 on any
  python3 -I - "$1" <<'PY'
import sys, glob, os, yaml
SANCTIONED = ("${{ fromJSON(((github.event_name != 'pull_request' || github.event.pull_request.head.repo.full_name == github.repository) "
              "&& vars.CI_RUNNER) || '\"ubuntu-latest\"') }}")
HOSTED = {"ubuntu-latest", "windows-latest", "macos-latest"}
BAD_TRIGGERS = {"pull_request_target", "workflow_run"}
bad = 0
def fail(f, msg):
    global bad
    bad = 1
    print(f"FAIL  {f}: {msg}")
files = sorted(glob.glob(os.path.join(sys.argv[1], "*.yml")) + glob.glob(os.path.join(sys.argv[1], "*.yaml")))
for f in files:
    wf = yaml.safe_load(open(f))
    if not isinstance(wf, dict):
        fail(f, "not a mapping"); continue
    on = wf.get("on", wf.get(True))  # YAML 1.1 parses the key `on` as True
    if isinstance(on, str): triggers = {on}
    elif isinstance(on, list): triggers = {str(x) for x in on}
    elif isinstance(on, dict): triggers = {str(x) for x in on}
    else: triggers = set(); fail(f, "unreadable `on:`")
    for t in sorted(triggers & BAD_TRIGGERS):
        fail(f, f"triggers on {t}")
    for name, job in (wf.get("jobs") or {}).items():
        if "uses" in job and "runs-on" not in job:
            continue  # reusable workflow call: the callee's runs-on is checked in its own file
        ro = job.get("runs-on")
        if ro == SANCTIONED or (isinstance(ro, str) and ro in HOSTED):
            continue
        if ro == "${{ matrix.os }}":
            m = (job.get("strategy") or {}).get("matrix") or {}
            vals = list(m.get("os") or []) + [i.get("os") for i in m.get("include") or [] if "os" in i]
            if vals and all(v in HOSTED for v in vals):
                continue
            fail(f, f"job {name}: matrix.os values {vals} are not all hosted labels"); continue
        fail(f, f"job {name}: unsanctioned runs-on {ro!r}")
sys.exit(bad)
PY
}

fail=0
if check .github/workflows; then
  echo "ok    workflows: no pull_request_target/workflow_run, every runs-on sanctioned or hosted"
else
  fail=1
fi

# Mutation fixtures: each must be rejected.
d="$(mktemp -d)"
trap 'find "$d" -mindepth 1 -delete; rmdir "$d"' EXIT
sanctioned="\${{ fromJSON(((github.event_name != 'pull_request' || github.event.pull_request.head.repo.full_name == github.repository) && vars.CI_RUNNER) || '\"ubuntu-latest\"') }}"
mutant() { # NAME ON-BLOCK RUNS-ON-BLOCK [EXTRA-JOB-LINES]
  local m="$d/$1"
  mkdir -p "$m"
  printf 'name: x\non:%s\njobs:\n  a:\n    runs-on:%s\n    steps:\n      - run: true\n%s' "$2" "$3" "${4:-}" >"$m/w.yml"
  if check "$m" >/dev/null; then echo "FAIL  mutation not caught: $1"; fail=1; else echo "ok    rejects $1"; fi
}
# a control: the same scaffold with sanctioned values passes
mkdir -p "$d/control"
printf 'name: x\non: [push]\njobs:\n  a:\n    runs-on: %s\n    steps:\n      - run: true\n' "$sanctioned" >"$d/control/w.yml"
if check "$d/control" >/dev/null; then echo "ok    accepts the sanctioned scaffold"; else echo "FAIL  sanctioned scaffold rejected"; fail=1; fi

mutant custom-label        ' push'                          ' monoes-local'
mutant custom-label-list   ' push'                          ' [monoes-vps]'
mutant self-hosted-list    ' push'                          ' [self-hosted, linux]'
mutant runner-group        ' push'                          $'\n      group: x'
mutant other-variable      ' push'                          " \${{ fromJSON(vars.OTHER) }}"
mutant ci-runner-variant   ' push'                          " \${{ fromJSON(vars.CI_RUNNER) }}"
mutant on-flow-list        ' [push, workflow_run]'          ' ubuntu-latest'
mutant on-block-mapping    $'\n  workflow_run:\n    workflows: [CI]' ' ubuntu-latest'
mutant on-string           ' workflow_run'                  ' ubuntu-latest'
mutant on-block-list       $'\n  - push\n  - pull_request_target' ' ubuntu-latest'
mutant on-target-mapping   $'\n  pull_request_target:'    ' ubuntu-latest'
mutant matrix-custom-os    ' push'                          ' ${{ matrix.os }}' $'    strategy:\n      matrix:\n        os: [ubuntu-latest, monoes-local]\n'
mutant matrix-no-os        ' push'                          ' ${{ matrix.os }}' $'    strategy:\n      matrix:\n        x: [1]\n'
mutant pinned-label        ' push'                          ' ubuntu-22.04'
exit $fail
