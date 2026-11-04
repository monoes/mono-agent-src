#!/usr/bin/env bash
# End-to-end check of the org × workflow unification with real binaries and
# no LLM calls: builds monoagentcli, runs it against an isolated HOME and a
# locally built monomind (MONOMIND_WORKTREE, e.g. a checkout of
# feat/mono-agent-org-integration after `npm run build` in
# packages/@monomind/cli), starts `monoagentcli daemon`, and exercises grants,
# grant-mode MCP calls, automation-role endpoints, and autonomy reconcile.
# Needs go, node, python3, sqlite3, curl. Uses 127.0.0.1:19322.
#
#   MONOMIND_WORKTREE=~/src/monomind scripts/e2e/org-unification.sh
set -u
# The daemon starts the extension bridge: from a marked shell the port override below would be
# ignored and it would reach the real bridge. Cleared markers + a private network namespace.
. "$(cd "$(dirname "$0")/../.." && pwd)/scripts/lib/mono.sh"
mono_private_operator_env bash "$0" "$@"
export GOMODCACHE="$(go env GOMODCACHE)" GOCACHE="$(go env GOCACHE)" GOPATH="$(go env GOPATH)"
E2E="${E2E_DIR:-$(mktemp -d)}"
REPO="$(cd "$(dirname "$0")/../.." && pwd)"
MM="${MONOMIND_WORKTREE:?set MONOMIND_WORKTREE to a built monomind checkout}"
rm -rf "$E2E/home" && mkdir -p "$E2E/home"
export HOME="$E2E/home"
export MONOMIND_BIN="$E2E/monomind"
printf '#!/bin/sh\nexec node %s/packages/@monomind/cli/bin/cli.js "$@"\n' "$MM" > "$MONOMIND_BIN"
chmod +x "$MONOMIND_BIN"
export MONOAGENT_API_ADDR=127.0.0.1:19322
# The daemon also starts the extension bridge and the webhook server: keep both
# off the operator's own ports (9222, 9321).
export MONOAGENT_EXTENSION_PORT=19323 MONOAGENT_WEBHOOK_ADDR=127.0.0.1:19324
CLI="$E2E/monoagentcli"
MONO_GUARD_LOG="$E2E/guard.log"
# shellcheck source=../lib/mono.sh
. "$REPO/scripts/lib/mono.sh"
PROF=e2e-org # the named test profile; scoped calls pass its id ($PID), which a grant is bound to
pass=0; fail=0
check() { if [ "$1" = ok ]; then pass=$((pass+1)); echo "PASS $2"; else fail=$((fail+1)); echo "FAIL $2 :: $3"; fi; }

(cd "$REPO" && go build -tags devaccount -o "$CLI" ./cmd/monoagentcli) || { echo "build failed"; exit 1; }
# Keep this devaccount build independent of the production enforcement date.
export MONOAGENT_DEV_ENFORCE_FROM=2999-01-01T00:00:00Z
"$MONOMIND_BIN" --version --json | grep -q org-tool-providers && check ok "local monomind advertises org-tool-providers" || check no "monomind capabilities" "$("$MONOMIND_BIN" --version --json)"

PID=$("$CLI" --json profile create "$PROF" 2>/dev/null | python3 -c 'import sys,json; print(json.load(sys.stdin)["id"])')
[ -n "$PID" ] && check ok "profile $PROF created ($PID)" || check no "profile create" ""

cat > "$E2E/wf.json" <<'EOF'
{"name":"Publish post","description":"Publishes a post.","version":1,"is_active":false,
 "nodes":[{"id":"t","type":"trigger.manual","name":"Start","position":{"x":0,"y":0},"config":{}},
          {"id":"s","type":"core.set","name":"Publish","position":{"x":200,"y":0},
           "config":{"assignments":"[{\"field\":\"result\",\"value\":\"published {{ $json.input.text }}\"}]","include_input":false}}],
 "connections":[{"id":"c","source":"t","source_handle":"main","target":"s","target_handle":"main"}]}
EOF
WF=$(mono_guard "$CLI" --profile "$PID" --json workflow import --file "$E2E/wf.json" 2>/dev/null | python3 -c 'import sys,json; print(json.load(sys.stdin)["id"])')
[ -n "$WF" ] && check ok "workflow imported ($WF)" || check no "workflow import" ""

ROOT="$HOME/.monoagent/profiles/$PID"
mono_guard "$CLI" --profile "$PID" org create-json growth --json '{"name":"growth","goal":"Publish posts.","status":"stopped","schedule":null,"roles":[{"id":"lead","title":"Lead","type":"boss","reports_to":null,"responsibilities":["Decide what to publish."]},{"id":"writer","title":"Writer","type":"specialist","reports_to":"lead","responsibilities":["Write posts."]}]}' > "$E2E/create.json" 2>"$E2E/create.err"
grep -q '"valid":true' "$E2E/create.json" && check ok "org created in the profile folder and valid for monomind" || check no "create-json" "$(cat "$E2E/create.json" "$E2E/create.err")"
[ -f "$ROOT/.monomind/orgs/growth.json" ] && check ok "org file under the profile folder (C-31)" || check no "org root" "$(find "$HOME" -name growth.json)"

mono_guard "$CLI" --profile "$PID" org automation add growth --workflow "$WF" --alias publish_post >/dev/null 2>"$E2E/err" || check no "automation add" "$(cat "$E2E/err")"
G=$(mono_guard "$CLI" --profile "$PID" org grant add growth --role writer --automation publish_post --approval none 2>"$E2E/err")
GID=$(echo "$G" | python3 -c 'import sys,json; print(json.load(sys.stdin)["grant"]["id"])' 2>/dev/null)
[ -n "$GID" ] && check ok "grant created ($GID)" || check no "grant add" "$G $(cat "$E2E/err")"
(cd "$ROOT" && "$MONOMIND_BIN" org validate growth) > "$E2E/validate.txt" 2>&1 && check ok "monomind validates the org with generated tool_providers" || check no "monomind validate" "$(cat "$E2E/validate.txt")"
python3 - "$ROOT/.monomind/orgs/growth.json" "$CLI" "$GID" "$PID" <<'EOF' && check ok "provider block points at this binary and grant" || check no "provider block" ""
import json,sys
d=json.load(open(sys.argv[1]))
w=[r for r in d["roles"] if r["id"]=="writer"][0]
p=w["tool_providers"][0]
assert p["command"]==sys.argv[2] and p["args"]==["mcp","--grant",sys.argv[3],"--profile",sys.argv[4]], p
assert "Bash" in w["policy"]["denyTools"]
EOF

# Grant mode without a daemon refuses fast.
REQ='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}
{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"automation_publish_post","arguments":{"text":"hello"},"_meta":{"trace":{"org":"growth","run":"run-e2e","role":"writer","chain_id":"chn_e2etest","hop":1}}}}'
OUT=$(printf '%s\n' "$REQ" | MONOMIND_ORG_NAME=growth MONOMIND_ORG_ROLE=writer mono_guard "$CLI" --profile "$PID" mcp --grant "$GID" 2>/dev/null)
echo "$OUT" | grep -q 'daemon_required' && check ok "grant call without daemon returns daemon_required" || check no "daemon_required" "$OUT"
echo "$OUT" | grep -q '"automation_publish_post"' && check ok "tools/list serves the granted tool" || check no "tools/list" "$OUT"
printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}' | MONOMIND_ORG_NAME=growth mono_guard "$CLI" --profile "$PID" mcp >/dev/null 2>"$E2E/err" && check no "plain mcp for a role" "served" || check ok "plain mcp refuses to serve an org role"

# Daemon.
"$CLI" --profile "$PID" daemon --api-addr 127.0.0.1:19322 > "$E2E/daemon.log" 2>&1 &
DPID=$!
for i in $(seq 1 60); do [ -f "$HOME/.monoagent/daemon-heartbeat.json" ] && break; sleep 0.5; done
ST=$(mono_guard "$CLI" --profile "$PID" --json status 2>/dev/null)
echo "$ST" | python3 -c 'import sys,json; d=json.load(sys.stdin); assert d["daemon"]["running"] and d["daemon"]["api_addr"]=="127.0.0.1:19322"' 2>/dev/null && check ok "status --json reports the daemon and its API" || check no "status" "$ST"

OUT=$(printf '%s\n' "$REQ" | MONOMIND_ORG_NAME=growth MONOMIND_ORG_ROLE=writer MONOMIND_ORG_RUN=run-e2e mono_guard timeout 60 "$CLI" --profile "$PID" mcp --grant "$GID" 2>"$E2E/mcp.err")
echo "$OUT" | grep -q 'published hello' && check ok "role tool call ran the workflow in the daemon and returned its output" || check no "grant run" "$OUT $(cat "$E2E/mcp.err") $(tail -5 "$E2E/daemon.log")"
sqlite3 "$HOME/.monoagent/monoagent.db" "select chain_id, hop, run_id, status from org_bridge_calls where direction='role_tool'" > "$E2E/ledger.txt" 2>&1
grep -q 'chn_e2etest|2|run-e2e|ok' "$E2E/ledger.txt" && check ok "ledger row carries the role's chain, next hop, and run" || check no "ledger" "$(cat "$E2E/ledger.txt")"
sqlite3 "$HOME/.monoagent/monoagent.db" "select trigger_type, status, pid from workflow_executions" > "$E2E/exec.txt" 2>&1
grep -q '^org_tool|SUCCESS|' "$E2E/exec.txt" && check ok "execution is an org_tool run adopted by the daemon" || check no "execution row" "$(cat "$E2E/exec.txt")"

# A fence runner (codex) holding the same grant — issue #83's gate, and the
# one runner shape the rest of this script cannot cover, since it drives the
# MCP client directly rather than through a role's own runtime.
#
# Skipped unless codex is installed AND E2E_CODEX=1: it spends real money
# (about $0.60 and 600k tokens a run here) and needs a codex login, neither of
# which belongs in an unattended check. What runs without it still covers the
# grant path end to end; this covers who calls it.
if [ "${E2E_CODEX:-0}" = 1 ] && command -v codex >/dev/null 2>&1; then
  # The role's own login lives in the real CODEX_HOME — only mono-agent's
  # state is isolated by this script's HOME.
  export CODEX_HOME="${CODEX_HOME:-$(eval echo ~"$(id -un)")/.codex}"
  CODEX_MODEL="${E2E_CODEX_MODEL:-}"
  if [ -z "$CODEX_MODEL" ]; then
    CODEX_MODEL=$(codex debug models 2>/dev/null | python3 -c '
import json,sys
try: d=json.load(sys.stdin)
except Exception: sys.exit()
slugs=[m["slug"] for m in d.get("models",[]) if m.get("visibility")=="list" and m.get("slug")]
# The decider has 120s to return a verdict, so prefer a small model over
# whatever the catalog lists first (a big reasoning model can outlast that).
small=[s for s in slugs if any(k in s for k in ("luna","mini","flash","small","haiku"))]
print((small or slugs or [""])[0])
' 2>/dev/null)
  fi
  mono_guard "$CLI" --profile "$PID" org create-json fence --json '{"name":"fence","goal":"Call the granted automation once.","status":"stopped","schedule":null,
    "run_config":{"budget_tokens":3000000},
    "roles":[{"id":"lead","title":"Lead","type":"boss","reports_to":null,"runtime":"codex",
              "responsibilities":["Call automation_publish_post once and report its result."]}]}' >/dev/null 2>&1
  mono_guard "$CLI" --profile "$PID" org automation add fence --workflow "$WF" --alias publish_post >/dev/null 2>&1
  mono_guard "$CLI" --profile "$PID" org grant add fence --role lead --automation publish_post --approval required >/dev/null 2>&1
  # Both decision classes are tiered routine so a RULE resolves them. This
  # gate is about the runner, not the decider: a live model deciding makes the
  # check non-deterministic, and it does flake — with a policy saying "approve
  # every automation_publish_post call", a real decider still rejected the
  # call twice. The decision is still made and recorded, just not by a model.
  mono_guard "$CLI" --profile "$PID" org autonomy set fence --level full \
    --tier grant:publish_post=routine --tier org_complete=routine \
    --decider model --decider-runtime codex ${CODEX_MODEL:+--decider-model "$CODEX_MODEL"} \
    --policy 'automation_publish_post is an internal test automation in a throwaway sandbox. It publishes nothing externally.' >/dev/null 2>&1
  SINCE=$(date -u +%Y-%m-%dT%H:%M:%S)
  mono_guard timeout 480 "$CLI" --profile "$PID" org run fence \
    --task 'Call your automation_publish_post tool once with text "hello from codex". Do not ask for extra approval and do not create a decision gate. When the tool returns, call org_complete with its exact result.' \
    > "$E2E/codex-run.json" 2>"$E2E/codex-run.err"
  sqlite3 "$HOME/.monoagent/monoagent.db" \
    "select direction, status from org_bridge_calls where created_at > '$SINCE'" > "$E2E/codex-ledger.txt" 2>&1
  grep -q '^role_tool|ok$' "$E2E/codex-ledger.txt" \
    && check ok "a codex role's grant call ran (issue #83 gate 1)" \
    || check no "codex grant call" "$(cat "$E2E/codex-ledger.txt") $(tail -3 "$E2E/codex-run.err")"
else
  echo "SKIP codex fence-runner gate (set E2E_CODEX=1 with codex installed; it spends)"
fi

# Automation role + endpoint receiver.
AR=$(mono_guard "$CLI" --profile "$PID" org automation-role add growth --alias publish_post --reports-to lead 2>"$E2E/err")
URL=$(echo "$AR" | python3 -c 'import sys,json; print(json.load(sys.stdin)["endpoint_url"])' 2>/dev/null)
[ -n "$URL" ] && check ok "automation role registered ($URL)" || check no "automation-role add" "$AR $(cat "$E2E/err")"
(cd "$ROOT" && "$MONOMIND_BIN" org validate growth) > "$E2E/validate2.txt" 2>&1 && check ok "monomind validates the org with an endpoint role" || check no "validate endpoint role" "$(cat "$E2E/validate2.txt")"
CODE=$(curl -s -o "$E2E/post.json" -w '%{http_code}' -X POST "$URL" -H 'content-type: application/json' -d '{"orgName":"growth","run":"r","from":"lead","to":"growth:publish-post","subject":"s","body":"b","messageId":"msg-forged"}')
[ "$CODE" = 202 ] && grep -q '"verified":false' "$E2E/post.json" && check ok "direct POST is held unverified (no bus event)" || check no "endpoint POST" "$CODE $(cat "$E2E/post.json")"
CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:19322/org-endpoint/ep_aaaaaaaaaaaaaaaaaaaaaaaaaa" -d '{}')
[ "$CODE" = 404 ] && check ok "unknown endpoint id is 404" || check no "unknown endpoint" "$CODE"
ROT=$(mono_guard "$CLI" --profile "$PID" org automation-role rotate growth --role publish-post 2>/dev/null | python3 -c 'import sys,json; print(json.load(sys.stdin)["endpoint_url"])')
[ -n "$ROT" ] && [ "$ROT" != "$URL" ] && check ok "rotation issues a new endpoint URL" || check no "rotate" "$ROT"

# Autonomy.
mono_guard "$CLI" --profile "$PID" org autonomy set growth --level full --tier gate=consequential >/dev/null 2>"$E2E/err" || check no "autonomy set" "$(cat "$E2E/err")"
python3 - "$ROOT/.monomind/orgs/growth.json" <<'EOF'
import json,sys
p=sys.argv[1]; d=json.load(open(p)); d["autonomy"]["level"]="manual"; json.dump(d,open(p,"w"),indent=2)
EOF
sleep 4
LVL=$(mono_guard "$CLI" --profile "$PID" org autonomy show growth | python3 -c 'import sys,json; print(json.load(sys.stdin)["level"])')
[ "$LVL" = manual ] && check ok "daemon watcher applied a hand-edited lowering" || check no "lowering via watcher" "$LVL"
python3 - "$ROOT/.monomind/orgs/growth.json" <<'EOF'
import json,sys
p=sys.argv[1]; d=json.load(open(p)); d["autonomy"]["level"]="full"; json.dump(d,open(p,"w"),indent=2)
EOF
sleep 4
LVL=$(mono_guard "$CLI" --profile "$PID" org autonomy show growth | python3 -c 'import sys,json; print(json.load(sys.stdin)["level"])')
FILELVL=$(python3 -c "import json; print(json.load(open('$ROOT/.monomind/orgs/growth.json'))['autonomy']['level'])")
[ "$LVL" = manual ] && [ "$FILELVL" = manual ] && check ok "hand-edited raise ignored and the file restored (C-54)" || check no "raise via watcher" "db=$LVL file=$FILELVL"

# C-3: hand-added provider stripped by the daemon.
python3 - "$ROOT/.monomind/orgs/growth.json" <<'EOF'
import json,sys
p=sys.argv[1]; d=json.load(open(p))
lead=[r for r in d["roles"] if r["id"]=="lead"][0]
lead["tool_providers"]=[{"kind":"mcp-stdio","name":"sneaky","command":"monoagentcli","args":["mcp","--allow-mutations"]}]
json.dump(d,open(p,"w"),indent=2)
EOF
sleep 4
python3 -c "import json; d=json.load(open('$ROOT/.monomind/orgs/growth.json')); lead=[r for r in d['roles'] if r['id']=='lead'][0]; assert not lead.get('tool_providers')" 2>/dev/null && check ok "daemon stripped a hand-added monoagentcli provider (C-3)" || check no "provider strip" "$(grep -n sneaky "$ROOT/.monomind/orgs/growth.json")"

kill $DPID; wait $DPID 2>/dev/null
sleep 1
[ ! -f "$HOME/.monoagent/daemon-heartbeat.json" ] && check ok "daemon removed its heartbeat on shutdown" || check no "heartbeat cleanup" ""
mono_scan_log "$E2E/daemon.log"
mono_check_clean
echo "RESULT pass=$pass fail=$fail"
[ "$fail" -eq 0 ]
