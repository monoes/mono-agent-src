#!/usr/bin/env bash
# Test kit for the A2UI chat surfaces (branch integration/a2ui).
#
# Everything runs against a throwaway home ($A2UI_TEST_DIR/home, default ~/scratch/a2ui-test) with a test build
# of the CLI, sign-in disabled, and the extension bridge on port 9232. Your real ~/.monoagent, installed app and
# live bridge are not used. Run it in YOUR OWN terminal: button presses are refused from an agent shell.
#
#   scripts/a2ui-testkit.sh setup          build the test CLI and create the test profile
#   scripts/a2ui-testkit.sh proof          CLI proof: forged, wrong, genuine and repeated button presses
#   scripts/a2ui-testkit.sh app            run the desktop app from this checkout (wails dev)
#   scripts/a2ui-testkit.sh seed [text]    add a pending approval to the test profile
#   scripts/a2ui-testkit.sh status         list the test approvals and what was decided
#   scripts/a2ui-testkit.sh bridge         run the test extension bridge on port 9232
#   scripts/a2ui-testkit.sh browser        open a separate Chromium profile with this checkout's extension
#   scripts/a2ui-testkit.sh pair           print the console line that points that browser at the test bridge
#   scripts/a2ui-testkit.sh cli <args...>  run the test CLI (add --profile a2ui-test)
#
# A2UI_USE_MY_CLAUDE=1 lets the test app and bridge use your Claude login (~/.claude) so a real model can answer.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
DIR=${A2UI_TEST_DIR:-$HOME/scratch/a2ui-test}
REAL_HOME=$HOME
TEST_HOME=$DIR/home
BIN=$DIR/bin/monoagentcli
DB=$TEST_HOME/.monoagent/monoagent.db
PORT=9232
PROFILE=a2ui-test

die() { echo "a2ui-testkit: $*" >&2; exit 1; }
need() { command -v "$1" >/dev/null || die "$1 is not installed"; }

# testenv runs a command in the test world. The Go and npm caches stay where they are.
testenv() {
	local extra=()
	[ "${A2UI_USE_MY_CLAUDE:-}" = 1 ] && extra=(CLAUDE_CONFIG_DIR="${CLAUDE_CONFIG_DIR:-$REAL_HOME/.claude}")
	env HOME="$TEST_HOME" MONOAGENT_DEV_ENFORCE_FROM=2999-01-01T00:00:00Z MONOES_BASE_URL=http://127.0.0.1:9 \
		MONOAGENT_EXTENSION_PORT=$PORT MONOAGENTCLI_BIN="$BIN" \
		GOPATH="$(go env GOPATH)" GOCACHE="$(go env GOCACHE)" GOMODCACHE="$(go env GOMODCACHE)" \
		npm_config_cache="${npm_config_cache:-$REAL_HOME/.npm}" "${extra[@]}" "$@"
}
# cli runs the test CLI with the start-up log lines removed.
cli() { testenv "$BIN" "$@" 2> >(grep -v 'login will be required\|applied migration\|storage:\|migrated ' >&2); }

profile_id() {
	[ -f "$DB" ] || die "run 'setup' first"
	sqlite3 "$DB" "select id from profiles where name='$PROFILE' limit 1"
}

# seed_approval <id> <editable text>
seed_approval() {
	local p text
	p=$(profile_id)
	text=${2//\\/\\\\}
	text=${text//\"/\\\"}
	text=${text//\'/\'\'}
	sqlite3 "$DB" "insert into hil_pending(id,execution_id,workflow_id,node_id,node_name,status,readonly_data,editable_data,created_at,profile_id)
		values('$1','ex-$1','wf-test','n1','Send DM to Sara','pending','{\"to\":\"@sara\",\"channel\":\"test only\"}','{\"text\":\"$text\"}',datetime('now'),'$p');"
}

cmd=${1:-help}
[ $# -gt 0 ] && shift

case $cmd in
setup)
	need go
	need sqlite3
	mkdir -p "$DIR/bin" "$TEST_HOME"
	chmod 755 "$DIR/bin"
	echo "building the test CLI (devaccount build, never install it)..."
	(cd "$ROOT" && go build -tags devaccount -o "$BIN" ./cmd/monoagentcli)
	chmod 755 "$BIN"
	[ -f "$DB" ] && [ -n "$(profile_id)" ] || cli profile create "$PROFILE" >/dev/null
	echo "ready. test home: $TEST_HOME   profile: $PROFILE ($(profile_id))"
	;;
seed)
	id=t-$(date +%H%M%S)
	seed_approval "$id" "${1:-Hi Sara, thanks for commenting!}"
	echo "added pending approval $id (card id hil-$id). An open chat that has at least one message shows it within 10 seconds."
	;;
status)
	[ -f "$DB" ] || die "run 'setup' first"
	sqlite3 -header -column "$DB" "select id, status, edited_data from hil_pending order by created_at"
	;;
proof)
	need unshare
	[ -z "${CLAUDECODE:-}" ] || die "this shell belongs to an agent (CLAUDECODE is set); run the proof in your own terminal"
	p=$(profile_id)
	n=$(date +%H%M%S)
	conv=proof-$n
	id=p-$n
	sqlite3 "$DB" "insert into ai_chat_conversations(id,profile_id,backend,created_at,updated_at,mode,cwd) values('$conv','$p','agent',datetime('now'),datetime('now'),'coder','$DIR');"
	seed_approval "$id" "Hi Sara, thanks for commenting!"
	# the proof runs with no network at all
	iso() { testenv unshare -rn "$BIN" "$@" 2>&1 | grep -v 'login will be required' || true; }
	press() { # name component context
		printf '{"version":"1.0","action":{"name":"%s","surfaceId":"hil-%s","sourceComponentId":"%s","timestamp":"2026-01-01T00:00:00Z","context":%s}}' "$1" "$id" "$2" "$3" |
			iso chat ui action --action - --profile "$p" -- "$conv" | tr -d '\n'
		echo
		echo "   approval status: $(sqlite3 "$DB" "select status || '  edited=' || edited_data from hil_pending where id='$id'")"
	}
	echo "0a) sync before the chat has a message: expect nothing surfaced"
	iso chat ui sync-hil "$conv" --profile "$p" | tr -d '\n '; echo
	sqlite3 "$DB" "insert into ai_chat_turns(id,conversation_id,profile_id,owner_instance_id,prompt,status,reason,last_committed_seq,created_at,updated_at) values('work-$n','$conv','$p','','hello','completed','',0,strftime('%Y-%m-%dT%H:%M:%SZ','now'),strftime('%Y-%m-%dT%H:%M:%SZ','now'));"
	echo "0b) sync after a message: expect the older test cards and hil-$id surfaced"
	iso chat ui sync-hil "$conv" --profile "$p" | tr -d '\n '; echo
	echo "1) forged item id: expect rejected, still pending"
	press hil.approve btn-approve '{"id":"someone-else","text":"x"}'
	echo "2) wrong action name for that button: expect rejected, still pending"
	press hil.reject btn-approve "{\"id\":\"$id\"}"
	echo "3) button that does not exist: expect rejected, still pending"
	press hil.approve btn-nope "{\"id\":\"$id\",\"text\":\"x\"}"
	echo "4) genuine Approve with edited text: expect accepted, approved, edited text stored"
	press hil.approve btn-approve "{\"id\":\"$id\",\"text\":\"EDITED by the proof\"}"
	echo "5) second press: expect stale or rejected, nothing changes"
	press hil.approve btn-approve "{\"id\":\"$id\",\"text\":\"second\"}"
	echo "6) sync again: expect nothing new for hil-$id"
	iso chat ui sync-hil "$conv" --profile "$p" | tr -d '\n '; echo
	echo "7) the text view of the chat's cards"
	iso chat ui show "$conv" --profile "$p"
	;;
app)
	need wails
	[ -x "$BIN" ] || die "run 'setup' first"
	cd "$ROOT/wails-app"
	[ -d frontend/node_modules ] || (cd frontend && npm ci)
	testenv wails dev -tags "webkit2_41 devaccount"
	echo "wails may have rewritten generated files; 'git status' shows them, discard them if so."
	;;
bridge)
	[ -x "$BIN" ] || die "run 'setup' first"
	testenv "$BIN" extension serve
	;;
browser)
	b=${A2UI_BROWSER:-chromium}
	need "$b"
	mkdir -p "$DIR/browser"
	exec "$b" --user-data-dir="$DIR/browser" --load-extension="$ROOT/chrome-extension" --no-first-run
	;;
pair)
	[ -x "$BIN" ] || die "run 'setup' first"
	echo "Run 'bridge' first. Then print the token with:"
	echo "  scripts/a2ui-testkit.sh cli extension pair"
	echo "In the TEST browser open chrome://extensions, enable Developer mode, open the extension's service worker console and run:"
	echo "  chrome.storage.local.set({wsUrl:'ws://127.0.0.1:$PORT/monoagent', pairingToken:'<token>'})"
	;;
cli)
	[ -x "$BIN" ] || die "run 'setup' first"
	cli "$@"
	;;
*)
	sed -n '2,19p' "$0" | sed 's/^# \{0,1\}//'
	;;
esac
