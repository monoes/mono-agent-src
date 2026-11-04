# Shared settings for the automation e2e scripts. Source it; don't run it.
#
# Everything lives under a scratch directory and talks to a PRIVATE bridge:
# never the user's bridge on 9222 (it holds their real browser's extension
# connection, and the newest paired socket wins).

E2E_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$E2E_DIR/../.." && pwd)"

: "${E2E_WORK:=$HOME/scratch/automation-e2e-run}"    # scratch root for this run
: "${E2E_HOME:=$E2E_WORK/home}"                         # HOME the CLI and bridge use
: "${E2E_BIN:=$E2E_WORK/monoagentcli}"                  # binary under test
: "${E2E_BRIDGE_PORT:=9232}"                            # private bridge port
: "${E2E_CDP_PORT:=9447}"                               # test browser's DevTools port
: "${E2E_FIXTURE_PORT:=18765}"                          # fixture CRM site
: "${E2E_EXTENSION_DIR:=$REPO_ROOT/chrome-extension}"   # extension under test
: "${E2E_CHROME:=}"                                     # browser binary (default: first found)
export E2E_DIR REPO_ROOT E2E_WORK E2E_HOME E2E_BIN E2E_BRIDGE_PORT E2E_CDP_PORT E2E_FIXTURE_PORT E2E_EXTENSION_DIR E2E_CHROME

# setup.sh builds with devaccount; rebuild older binaries with E2E_BUILD=1.
export MONOAGENT_DEV_ENFORCE_FROM=2999-01-01T00:00:00Z

# The fixture's login; recordings and runs must never leak it.
export E2E_PASSWORD='S3cr3t-Pa55word!'
export E2E_SITE="http://crm.e2e.test:$E2E_FIXTURE_PORT"

# The port override is honoured only for the operator's own terminal; from a marked
# shell the CLI would fall back to the real bridge on 9222/9323. This stack starts
# daemons that later scripts reach, so it needs ONE private network namespace for
# the whole run (see mono_require_private_net), not one per script.
# shellcheck source=../../scripts/lib/mono.sh
. "$REPO_ROOT/scripts/lib/mono.sh"
mono_require_private_net

for p in "$E2E_BRIDGE_PORT" "$E2E_CDP_PORT" "$E2E_FIXTURE_PORT"; do
  if [ "$p" = 9222 ]; then
    echo "e2e: refusing to use port 9222 (the user's bridge)" >&2
    return 1 2>/dev/null || exit 1
  fi
done

mkdir -p "$E2E_WORK" "$E2E_HOME/.monoagent" "$E2E_WORK/tmp" "$E2E_WORK/bin"
export GOTMPDIR="$E2E_WORK/tmp" TMPDIR="$E2E_WORK/tmp"

# The profile every scoped call acts on, and the guard that fails the run when
# one forgets it (scripts/lib/mono.sh). Call mono_check_clean at the end.
: "${E2E_PROFILE:=e2e}"
export E2E_PROFILE
# shellcheck source=../../scripts/lib/mono.sh
. "$REPO_ROOT/scripts/lib/mono.sh"

# An xdg-open that only logs: nothing here may open a page in the user's browser.
if [ ! -x "$E2E_WORK/bin/xdg-open" ]; then
  printf '#!/bin/sh\necho "$(date -Is) xdg-open $*" >> "%s/xdg-open.log"\n' "$E2E_WORK" > "$E2E_WORK/bin/xdg-open"
  chmod +x "$E2E_WORK/bin/xdg-open"
fi

# m runs the binary under test against the scratch HOME and private bridge.
m() {
  HOME="$E2E_HOME" MONOAGENT_EXTENSION_PORT="$E2E_BRIDGE_PORT" PATH="$E2E_WORK/bin:$PATH" mono_guard "$E2E_BIN" "$@"
}

# mt <seconds> <args…> is m with a time limit.
mt() {
  local t="$1"; shift
  mono_guard timeout "$t" env HOME="$E2E_HOME" MONOAGENT_EXTENSION_PORT="$E2E_BRIDGE_PORT" PATH="$E2E_WORK/bin:$PATH" "$E2E_BIN" "$@"
}

# ensure_profile <wrapper…> creates the named test profile in the HOME that
# wrapper (m, ml, mr, …) runs against, unless it is already there.
ensure_profile() {
  "$@" --json profile get "$E2E_PROFILE" >/dev/null 2>&1 || "$@" --json profile create "$E2E_PROFILE" >/dev/null
}

# j prints a Python expression over a JSON file (d is the document).
j() { python3 -c "import json,sys; d=json.load(open(sys.argv[1])); print($2)" "$1" 2>/dev/null; }

FAILS=0
# check <name> <got> <want> [detail]
check() {
  if [ "$2" = "$3" ]; then
    echo "PASS $1${4:+ :: $4}"
  else
    echo "FAIL $1 :: want [$3] got [$2]${4:+ $4}"
    FAILS=$((FAILS + 1))
  fi
}
