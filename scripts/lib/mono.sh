#!/usr/bin/env bash
# Profile guard for scripts that drive monoagentcli. Source it, then run every
# invocation through mono_guard (or mono, for the plain binary):
#
#   . "$(dirname "$0")/lib/mono.sh"
#   MONO_BIN=/path/to/monoagentcli
#   mono --profile smoke task list
#   mono_guard env HOME="$h" "$cli" --profile smoke task list   # any command line
#   mono_check_clean                                            # last line of the script
#
# A command that acts on a profile and was not told which one prints
# "this command acts on a profile" on stderr (internal/cli/profilegate). The
# guard passes stderr through unchanged, and when it contains that text it
# prints an ::error:: line, records the invocation in $MONO_GUARD_LOG and makes
# the call return 97. A call inside $(...) or a pipeline cannot stop the script,
# so mono_check_clean, called once at the end, fails the whole script (exit 97)
# for any recorded violation. Background processes whose stderr goes to a log
# are checked with mono_scan_log <file>.
MONO_GUARD_NEEDLE='this command acts on a profile'
: "${MONO_GUARD_LOG:=$(mktemp "${TMPDIR:-/tmp}/mono-guard.XXXXXX")}"
export MONO_GUARD_LOG

mono_guard() {
  local errf rc=0 oldtraps
  errf="$(mktemp "${TMPDIR:-/tmp}/mono-stderr.XXXXXX")"
  # Do not leave the capture file behind when the call is interrupted.
  oldtraps="$(trap -p INT TERM HUP)"
  trap 'rm -f "$errf"; exit 130' INT TERM HUP
  # stderr goes through tee: it is shown as the command writes it (a server
  # that runs for minutes is not silent) and captured for the check below.
  # stdout is carried around the pipe on fd 3.
  { "$@" 2>&1 1>&3 3>&- | tee "$errf" >&2; rc="${PIPESTATUS[0]}"; } 3>&1
  if grep -q "$MONO_GUARD_NEEDLE" "$errf"; then
    printf '%s\n' "$*" >>"$MONO_GUARD_LOG"
    echo "::error::profile guard: this call named no profile: $*" >&2
    rc=97
  fi
  rm -f "$errf"
  trap - INT TERM HUP
  [ -z "$oldtraps" ] || eval "$oldtraps"
  return "$rc"
}

mono() { mono_guard "${MONO_BIN:?set MONO_BIN to the monoagentcli binary}" "$@"; }

mono_scan_log() { # file: a background process's stderr
  if [ -f "$1" ] && grep -q "$MONO_GUARD_NEEDLE" "$1"; then
    echo "$1" >>"$MONO_GUARD_LOG"
    echo "::error::profile guard: $1 contains the profile warning" >&2
  fi
}

mono_check_clean() {
  if [ -s "$MONO_GUARD_LOG" ]; then
    echo "::error::profile guard: $(wc -l <"$MONO_GUARD_LOG") call(s) named no profile:" >&2
    cat "$MONO_GUARD_LOG" >&2
    exit 97
  fi
}

# mono_clear_markers: unset every agent/run marker (this list is
# operator.MarkerEnv(); internal/operator's TestMonoShMarkerListMatches holds it equal).
mono_clear_markers() {
  local v
  for v in CLAUDECODE CLAUDE_CODE_ENTRYPOINT MONOMIND_ORG_ROLE MONOMIND_SDK_AGENT MONOMIND_AGENT_EXEC AI_AGENT AGENT \
    CODEX_SANDBOX CODEX_SANDBOX_NETWORK_DISABLED CODEX_THREAD_ID CODEX_CI OPENCODE OPENCODE_PID ANTIGRAVITY_AGENT GEMINI_CLI \
    GROK_SESSION_ID GROK_MANAGED_BY_NPM COPILOT_CLI_BINARY_VERSION COPILOT_AGENT_SESSION_ID CRUSH PI_CODING_AGENT QWEN_CODE \
    PI_SESSION_ID DSH_SHELL DSH_SESSION_ID MONOMIND_CLINE_TURN MONOMIND_AIDER MONOAGENT_AGENT_CONTEXT MONOAGENT_ACTOR \
    MONOAGENT_WORKFLOW_RUN MONOAGENT_AI_CALLER MONOAGENT_AGENT_KIND MONOAGENT_AGENT_KIND_TOKEN; do
    unset "$v"
  done
}

# mono_in_private_net: true inside a network namespace with nothing but loopback,
# where the user's real bridge on 9222/9323 does not exist.
mono_in_private_net() {
  # a fresh namespace has only `lo` (reading /proc/1/ns/net is not allowed from inside a user namespace)
  command -v ip >/dev/null 2>&1 && ip -o link show 2>/dev/null | awk -F': ' '$2 !~ /^lo(@|$)/ {other=1} END {exit other}' && ip -o link show lo >/dev/null 2>&1
}

# mono_private_operator_env: for a script that runs a PRIVATE bridge on its own port
# (MONOAGENT_EXTENSION_PORT). The variable is honoured only for the operator's own
# terminal, so a script run from an agent's shell would fall back to the default
# ports (the real bridge). So: every marker is cleared, and the script re-runs
# itself in a private network namespace (loopback only). The namespace is entered
# with root mapping just to bring `lo` up; the script itself then runs as the
# current user (Chromium refuses to run as root). Call first:
#   mono_private_operator_env bash "$0" "$@"
# Without `unshare` (macOS) the script is SKIPPED (exit 0) with a message.
mono_private_operator_env() {
  mono_clear_markers
  mono_in_private_net && return 0
  if ! command -v unshare >/dev/null 2>&1 || [ ! -r /proc/self/ns/net ]; then
    echo "SKIP: no network namespaces here (no unshare): this script would reach the real extension bridge from a marked shell, so it does not run" >&2
    exit 0
  fi
  local uid gid
  uid="$(id -u)"; gid="$(id -g)"
  exec unshare -rn sh -c 'ip link set lo up || { echo "cannot bring loopback up in the private namespace" >&2; exit 97; }
    u="$1"; g="$2"; shift 2; exec unshare --map-user="$u" --map-group="$g" "$@"' sh "$uid" "$gid" "$@"
}

# mono_require_private_net: for a multi-script stack (setup.sh starts daemons that
# the next script talks to, so one namespace per script cannot work): refuse
# unless the whole stack runs from a shell that is already in a private network
# namespace (see mono_private_shell). Skips on macOS.
mono_require_private_net() {
  mono_clear_markers
  mono_in_private_net && return 0
  if ! command -v unshare >/dev/null 2>&1 || [ ! -r /proc/self/ns/net ]; then
    echo "SKIP: no network namespaces here (no unshare): this stack would reach the real extension bridge from a marked shell, so it does not run" >&2
    exit 0
  fi
  echo "refusing: run this e2e stack from a private network namespace so a marked shell cannot fall back to the real bridge on 9222/9323:" >&2
  echo "  unshare -rn sh -c 'ip link set lo up; exec unshare --map-user=\$(id -u) --map-group=\$(id -g) bash'   (then run setup.sh, regress-*.sh, teardown.sh there)" >&2
  exit 97
}
