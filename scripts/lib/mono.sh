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

# mono_private_operator_env: for scripts that run a PRIVATE bridge on their own
# port (MONOAGENT_EXTENSION_PORT). The port variable is honoured only for the
# operator's own terminal, so (1) every agent/run marker is cleared (this list is
# operator.MarkerEnv(); internal/operator's TestMonoShMarkerListMatches holds it
# equal) and (2) the script re-runs itself in a private network namespace
# (`unshare -rn`, loopback only), so a process that is still counted as marked
# (a parent that is an agent) can never fall back to the default ports 9222/9323,
# the user's real bridge. Call it first: mono_private_operator_env "$0" "$@"
mono_private_operator_env() {
  local v
  for v in CLAUDECODE CLAUDE_CODE_ENTRYPOINT MONOMIND_ORG_ROLE MONOMIND_SDK_AGENT MONOMIND_AGENT_EXEC AI_AGENT AGENT \
    CODEX_SANDBOX CODEX_SANDBOX_NETWORK_DISABLED CODEX_THREAD_ID CODEX_CI OPENCODE OPENCODE_PID ANTIGRAVITY_AGENT GEMINI_CLI \
    GROK_SESSION_ID GROK_MANAGED_BY_NPM COPILOT_CLI_BINARY_VERSION COPILOT_AGENT_SESSION_ID CRUSH PI_CODING_AGENT QWEN_CODE \
    PI_SESSION_ID DSH_SHELL DSH_SESSION_ID MONOMIND_CLINE_TURN MONOMIND_AIDER MONOAGENT_AGENT_CONTEXT MONOAGENT_ACTOR \
    MONOAGENT_WORKFLOW_RUN MONOAGENT_AI_CALLER MONOAGENT_AGENT_KIND MONOAGENT_AGENT_KIND_TOKEN; do
    unset "$v"
  done
  if [ -z "${MONO_E2E_IN_NETNS:-}" ]; then
    command -v unshare >/dev/null || { echo "refusing: no unshare, cannot isolate from the real bridge" >&2; exit 97; }
    export MONO_E2E_IN_NETNS=1
    exec unshare -rn sh -c 'ip link set lo up 2>/dev/null; exec "$@"' sh "$@"
  fi
}
