#!/usr/bin/env bash
# Old binary against a database that holds v2 binding rows (peer-credential binding, migration 148).
#
# The binding stores its tokens in tables an older binary does not read. This script proves the previous
# release copes with a database that has them: it starts and opens the database, it never accepts a v2
# secret as a valid token (a v2 secret is refused exactly as an unknown one is, while a legacy token still
# binds), its token-revoking path (a conversation delete) works and leaves the v2 rows alone, and the v2
# tables are byte for byte the same afterwards, their insert-only triggers still refusing.
#
#   scripts/procbind-old-binary-smoke.sh --new NEW_BIN (--old OLD_BIN | --old-tag TAG | --fetch-latest) [--require-reachable]
#   scripts/procbind-old-binary-smoke.sh --selftest --new NEW_BIN (--old OLD_BIN | --old-tag TAG)
#
#   --new NEW_BIN       the build under test, built with `-tags devaccount` (it makes the database and runs the baseline)
#   --old OLD_BIN       the previous release's binary
#   --old-tag TAG       build the previous release from its git tag with `-tags devaccount` (needs the tag and a Go toolchain)
#   --fetch-latest      download the latest published release binary from the public releases (no secret needed), check
#                       its sha256 against the release manifest, and SKIP, visibly, if any of that fails
#   --require-reachable fail, not just say so, when the old binary refuses commands for want of a monoes.me sign-in
#   --selftest          prove the checks catch a bad old binary (two negative controls) and exit
#
# A published release binary asks for a monoes.me sign-in on every command but a few (version, account, mcp starting),
# and a scratch run has none, so against it only the checks that need no sign-in are reachable: this script prints
# "NOT REACHABLE" for the rest. --old-tag builds the same code with the dev gate switched off, which reaches everything.
# The seeder (scripts/procbind-seed) is a test helper behind a build tag and is never part of a build.
#
# Scratch only: a fresh HOME under TMPDIR, no network (MONOES_BASE_URL points at a closed port); the real
# ~/.monoagent and the live bridge are never touched.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo="$(cd "$here/.." && pwd)"
new="" old="" tag="" fetch=0 require=0 selftest=0
while [ $# -gt 0 ]; do
  case "$1" in
    --new) new="${2:?}"; shift 2 ;;
    --old) old="${2:?}"; shift 2 ;;
    --old-tag) tag="${2:?}"; shift 2 ;;
    --fetch-latest) fetch=1; shift ;;
    --require-reachable) require=1; shift ;;
    --selftest) selftest=1; shift ;;
    *) echo "usage: see the header of $0" >&2; exit 2 ;;
  esac
done
command -v jq >/dev/null || { echo "procbind old-binary smoke: jq is required" >&2; exit 2; }
[ -n "$new" ] || { echo "procbind old-binary smoke: --new NEW_BIN is required" >&2; exit 2; }
fail() { echo "procbind old-binary smoke: FAIL: $*" >&2; exit 1; }
notice() { echo "procbind old-binary smoke: $*"; }

root="$(mktemp -d "${TMPDIR:-/tmp}/procbind-old-smoke.XXXXXX")"
oldsrc=""
cleanup() {
  [ -z "$oldsrc" ] || git -C "$repo" worktree remove --force "$oldsrc" >/dev/null 2>&1 || true
  rm -r "$root" 2>/dev/null || true
}
trap cleanup EXIT

# --- the old binary -----------------------------------------------------------------------------------------------------
if [ "$fetch" = 1 ]; then
  fetched=""
  if command -v gh >/dev/null && gh release download --repo monoes/mono-agent --pattern manifest.json --dir "$root" >/dev/null 2>&1; then
    os="$(uname -s | tr '[:upper:]' '[:lower:]')"; arch="$(uname -m)"
    case "$arch" in x86_64) arch=amd64 ;; aarch64) arch=arm64 ;; esac
    name="monoagentcli-$os-$arch"
    if gh release download --repo monoes/mono-agent --pattern "$name" --dir "$root" >/dev/null 2>&1; then
      want="$(jq -r --arg n "$name" '[.assets[] | select(.name == $n)][0].sha256 // empty' "$root/manifest.json")"
      got="$(sha256sum "$root/$name" | cut -d' ' -f1)"
      if [ -n "$want" ] && [ "$want" = "$got" ]; then chmod +x "$root/$name"; old="$root/$name"; fetched=1; fi
    fi
  fi
  [ -n "$fetched" ] || { notice "SKIP: could not fetch the latest release, or its sha256 did not match the manifest; nothing was checked"; exit 0; }
  notice "fetched $(jq -r .version "$root/manifest.json") ($name, sha256 verified against the manifest)"
fi
if [ -n "$tag" ]; then
  oldsrc="$root/oldsrc"
  git -C "$repo" fetch origin tag "$tag" >/dev/null 2>&1 || true
  git -C "$repo" worktree add --detach "$oldsrc" "$tag" >/dev/null 2>&1 || fail "no git tag $tag"
  (cd "$oldsrc" && go build -tags devaccount -o "$root/old-dev" ./cmd/monoagentcli) || fail "building $tag"
  old="$root/old-dev"
fi
[ -n "$old" ] || { echo "procbind old-binary smoke: one of --old, --old-tag or --fetch-latest is required" >&2; exit 2; }
[ -x "$new" ] && [ -x "$old" ] || fail "--new and the old binary must be executable files"

# --- scratch HOME, one runner for each binary ---------------------------------------------------------------------------
home="$root/home"; mkdir -p "$home" "$root/tmp"
db="$home/.monoagent/monoagent.db"
bin() { # BIN args... : a clean environment, no network, the scratch database
  local b="$1"; shift
  env -i "PATH=$PATH" "HOME=$home" "TMPDIR=$root/tmp" "MONOES_BASE_URL=http://127.0.0.1:9" "MONOAGENT_DEV_ENFORCE_FROM=2999-01-01T00:00:00Z" \
    "PROCBIND_SMOKE_SECRETS=${PROCBIND_SMOKE_SECRETS:-}" "PROCBIND_SMOKE_SEED=${PROCBIND_SMOKE_SEED:-}" "PROCBIND_SMOKE_DB=${PROCBIND_SMOKE_DB:-}" \
    ${EXTRA_ENV:-} "$b" --db-path "$db" "$@"
}
(cd "$repo" && go build -tags procbindseed -o "$root/procbind-seed" ./scripts/procbind-seed) || fail "building the seeder"
seed() { "$root/procbind-seed" -db "$db" "$@"; }

# --- the database: made by the new build, then the synthetic rows ------------------------------------------------------
bin "$new" --json profile create smoke >/dev/null 2>"$root/err" || fail "the new build cannot make a profile (is it a -tags devaccount build?): $(tail -3 "$root/err")"
pid="$(bin "$new" --json profile list 2>/dev/null | jq -r '.[] | select(.name == "smoke") | .id')"
[ -n "$pid" ] || fail "no profile id"
s="$(bin "$new" --profile smoke --json task session new "compat" --goal "old binary against v2 rows" 2>/dev/null | jq -r .session.id)"
t="$(bin "$new" --profile smoke --json task add "a task" --session "$s" --acceptance "done" 2>/dev/null | jq -r .task.id)"
bin "$new" --profile smoke task session approve "$s" >/dev/null 2>&1 || fail "approving the session"
seed -profile "$pid" -session "$s" -task "$t" >"$root/secrets.json" || fail "seeding the v2 rows"
leg="$(jq -r .legacy_mission_secret "$root/secrets.json")"
v2m="$(jq -r .v2_mission_secret "$root/secrets.json")"
v2r="$(jq -r .v2_revoked_secret "$root/secrets.json")"
export PROCBIND_SMOKE_SECRETS="$root/secrets.json" PROCBIND_SMOKE_SEED="$root/procbind-seed" PROCBIND_SMOKE_DB="$db"

before="$(seed -mode fingerprint)"
[ "$(jq '.agent_tokens_v2 + .coder_session_tokens_v2 + .process_bindings + .token_revocations_v2 + .procbind_host' <<<"$before")" -ge 7 ] || fail "the seeder wrote too few rows: $before"
probe0="$(seed -mode probe)"
jq -e 'all(.[]; . == true)' <<<"$probe0" >/dev/null || fail "the insert-only triggers do not refuse on the fresh database: $probe0"

checks=0 unreachable=0
ok() { checks=$((checks + 1)); notice "ok    $*"; }
notreach() { unreachable=$((unreachable + 1)); notice "NOT REACHABLE  $* (the old binary wants a monoes.me sign-in)"; }

# --- 1. the old binary starts ------------------------------------------------------------------------------------------
bin "$old" version >"$root/v.out" 2>&1 || fail "the old binary does not start: $(head -3 "$root/v.out")"
ok "the old binary starts: $(head -1 "$root/v.out")"

# is every ordinary command refused for want of a sign-in?
gated=0
if ! bin "$old" --json profile list >"$root/pl.out" 2>"$root/pl.err"; then
  if grep -q "Log in to monoes.me" "$root/pl.out" "$root/pl.err"; then gated=1; else fail "old 'profile list' failed: $(head -3 "$root/pl.err")"; fi
fi

# --- 2. it opens the database and answers over MCP -----------------------------------------------------------------------
mcp() { # BIN SECRET : initialize, then the session and a task, as a bound or unbound agent of the profile
  {
    echo '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"compat","version":"1"}}}'
    echo '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"task_session_get","arguments":{"profile":"smoke","id":'"$s"'}}}'
  } | EXTRA_ENV="MONOAGENT_TASK_BINDING=$2" bin "$1" mcp --tasks-only 2>/dev/null
}
mcp "$old" "" | jq -e 'select(.id == 1) | .result' >/dev/null || fail "the old binary's MCP server does not answer over the database with the v2 rows"
ok "the old binary opens the database with the v2 rows and answers over MCP"
if [ "$gated" = 0 ]; then
  jq -e 'length >= 2' <(bin "$old" --json profile list 2>/dev/null) >/dev/null || fail "old 'profile list' did not list the profiles"
  ok "the old binary lists the profiles"
fi

# --- 3. it never takes a v2 secret for a valid token ---------------------------------------------------------------------
verdict() { mcp "$old" "$1" | jq -c 'select(.id == 2) | {isError: .result.isError, text: .result.content[0].text}'; }
if [ "$gated" = 1 ]; then
  notreach "token verification (legacy binds, v2 refused)"
else
  vl="$(verdict "$leg")"; vm="$(verdict "$v2m")"; vr="$(verdict "$v2r")"; vg="$(verdict "mat2_not-a-token")"
  jq -e '.isError == false' <<<"$vl" >/dev/null || fail "a legacy mission token no longer binds in the old binary: $vl"
  ok "a legacy mission token still binds"
  for x in "$vm" "$vr"; do
    jq -e '.isError == true' <<<"$x" >/dev/null || fail "the old binary accepted a v2 secret: $x"
    [ "$x" = "$vg" ] || fail "a v2 secret is not refused the way an unknown token is: $x versus $vg"
  done
  ok "a v2 mission secret (live or revoked) is refused exactly as an unknown token is"
fi

# --- 4. its revoking path (a conversation delete) works and leaves the v2 rows alone -----------------------------------------
if [ "$gated" = 1 ]; then
  notreach "conversation delete (the old binary's token-revoking path)"
else
  cid="$(bin "$old" --profile smoke --json chat history create --runtime claude 2>/dev/null | jq -r '.id // .conversation.id // empty')"
  [ -n "$cid" ] || fail "the old binary could not create a conversation"
  bin "$old" --profile smoke --json chat history delete "$cid" >/dev/null 2>"$root/del.err" || fail "the old binary could not delete a conversation over a database with v2 rows: $(head -3 "$root/del.err")"
  ok "a conversation delete (revoke and sweep) works in the old binary"
fi

# --- 5. the v2 rows are untouched and the triggers still hold -------------------------------------------------------------
after="$(seed -mode fingerprint)"
[ "$before" = "$after" ] || fail "the old binary changed the v2 tables: before $before, after $after"
ok "the v2 tables are unchanged ($(jq -c 'del(.sha256)' <<<"$after"))"
probe1="$(seed -mode probe)"
jq -e 'all(.[]; . == true)' <<<"$probe1" >/dev/null || fail "an insert-only trigger no longer refuses after the old binary ran: $probe1"
ok "the insert-only triggers still refuse update and early removal"

# --- 6. the new build is unharmed -----------------------------------------------------------------------------------------
[ "$(mcp "$new" "$leg" | jq -r 'select(.id == 2) | .result.isError')" = "false" ] || fail "the new build no longer verifies the legacy token after the old binary ran"
ok "the new build still verifies the legacy token"

if [ "$unreachable" -gt 0 ] && [ "$require" = 1 ]; then
  fail "$unreachable check(s) were not reachable with this old binary and --require-reachable is set (use --old-tag)"
fi
notice "done: $checks checks passed, $unreachable not reachable"

# --- the script's own negative controls -----------------------------------------------------------------------------------
if [ "$selftest" = 1 ]; then
  [ "$gated" = 0 ] || fail "--selftest needs an old binary whose commands run (use --old-tag)"
  wrap="$root/wrap"; mkdir -p "$wrap"
  # (a) an old binary that takes a v2 secret for the legacy token: the verify check must fail
  cat >"$wrap/accepts-v2" <<EOF
#!/bin/sh
case "\${MONOAGENT_TASK_BINDING:-}" in mat2_*) MONOAGENT_TASK_BINDING="\$(jq -r .legacy_mission_secret "\$PROCBIND_SMOKE_SECRETS")"; export MONOAGENT_TASK_BINDING ;; esac
exec "$old" "\$@"
EOF
  # (b) an old binary that damages the v2 rows: the fingerprint check must fail
  cat >"$wrap/damages-rows" <<EOF
#!/bin/sh
"$old" "\$@"; rc=\$?
case "\$*" in *"chat history delete"*) "\$PROCBIND_SMOKE_SEED" -db "\$PROCBIND_SMOKE_DB" -mode tamper >/dev/null 2>&1 ;; esac
exit \$rc
EOF
  chmod +x "$wrap/accepts-v2" "$wrap/damages-rows"
  for w in accepts-v2:"accepted a v2 secret" damages-rows:"changed the v2 tables"; do
    if "$0" --new "$new" --old "$wrap/${w%%:*}" >"$root/neg.out" 2>&1; then fail "negative control ${w%%:*}: the script passed a bad old binary"; fi
    grep -q "${w#*:}\|not refused the way\|a legacy mission token no longer binds" "$root/neg.out" || fail "negative control ${w%%:*} failed for another reason: $(tail -2 "$root/neg.out")"
    ok "negative control ${w%%:*} is caught"
  done
  notice "selftest done"
fi
