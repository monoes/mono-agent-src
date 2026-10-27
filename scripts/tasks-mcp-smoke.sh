#!/usr/bin/env bash
# Smoke test for `monoagentcli mcp --tasks-only` over stdio, against a throwaway
# HOME and database: initialize, tools/list (exactly the task_* tools), the
# mutating verbs gated by --allow-mutations, claim -> comment -> finish, a
# task session worked through the tools (dependencies, evidence, no self-verification, a session-bound
# server held inside its session), a non-task tool refused by name, the CLI showing what the agent did, and the
# --api-only combination refused. Never touches the real ~/.monoagent.
#
#   go build -tags devaccount -o /tmp/monoagentcli ./cmd/monoagentcli && scripts/tasks-mcp-smoke.sh /tmp/monoagentcli
set -euo pipefail

cli="${1:?usage: tasks-mcp-smoke.sh <path to monoagentcli>}"
command -v jq >/dev/null || { echo "tasks mcp smoke: jq is required"; exit 1; }
root="$(mktemp -d)"
home="$root/home"
mkdir -p "$home/.claude" "$root/tmp"
trap 'rm -r "$root" 2>/dev/null || true' EXIT
export HOME="$home" USERPROFILE="$home" TMPDIR="$root/tmp" \
  XDG_CONFIG_HOME="$home/.config" XDG_CACHE_HOME="$home/.cache" XDG_DATA_HOME="$home/.local/share" XDG_STATE_HOME="$home/.local/state"
db="$home/.monoagent/monoagent.db"
# Use the devaccount build above: this smoke exercises task tools, independently of the gate.
# A clean environment, as in the operator's own terminal: the "straight to Ready" gate refuses when an
# agent marker (CLAUDECODE, AI_AGENT, ...) is set, and this script may itself run inside an agent's shell.
run() { mono_guard env -i "PATH=$PATH" "MONOAGENT_DEV_ENFORCE_FROM=2999-01-01T00:00:00Z" "HOME=$home" "TMPDIR=$root/tmp" "$cli" --db-path "$db" "$@"; }
# shellcheck source=lib/mono.sh
. "$(dirname "$0")/lib/mono.sh"
prof=smoke # the named profile every scoped call acts on
fail() { echo "tasks mcp smoke: $*" >&2; exit 1; }

# rpc <extra mcp flags> : reads request lines on stdin, prints responses. The server holds no profile:
# it takes no --profile, and every call below names its own in the "profile" argument.
rpc() { run mcp --tasks-only "$@"; }
init='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"smoke client","version":"1"}}}'
call() { jq -cn --argjson id "$1" --arg name "$2" --argjson args "$3" --arg p "$prof" '{jsonrpc:"2.0",id:$id,method:"tools/call",params:{name:$name,arguments:({profile:$p}+$args)}}'; }
# callp <id> <tool> <args> : the same without the profile argument added (for the refusal check)
callp() { printf '{"jsonrpc":"2.0","id":%s,"method":"tools/call","params":{"name":"%s","arguments":%s}}\n' "$1" "$2" "$3"; }
resp() { jq -c "select(.id==$2)" <<<"$1"; } # one response by id

run --json profile create "$prof" >/dev/null || fail "profile create"

echo "== the operator adds a task straight to Ready"
run --profile "$prof" task add "smoke test the task tools" --notes "from the smoke script" --ready >"$root/add.out" 2>&1 || fail "task add: $(cat "$root/add.out")"

echo "== a call without a profile is refused, naming the argument and listing the profiles"
out="$({ echo "$init"; callp 2 task_list '{}'; callp 3 task_list '{"profile":"nosuch"}'; call 4 task_list '{}'; } | rpc)"
resp "$out" 2 | jq -e --arg p "$prof" '.result.isError==true and (.result.content[0].text|contains("\"profile\" argument is required") and contains($p))' >/dev/null || fail "task_list without a profile: $(resp "$out" 2)"
resp "$out" 3 | jq -e '.result.isError==true and (.result.content[0].text|contains("not found"))' >/dev/null || fail "task_list with an unknown profile: $(resp "$out" 3)"
resp "$out" 4 | jq -e '.result.isError!=true' >/dev/null || fail "task_list with a profile: $(resp "$out" 4)"

echo "== without --allow-mutations: only the read tools are listed, a verb refuses"
out="$({ echo "$init"; echo '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'; call 3 task_claim '{"next":true}'; } | rpc)"
names="$(resp "$out" 2 | jq -r '[.result.tools[].name]|sort|join(",")')"
[ "$names" = "task_get,task_list,task_next,task_session_get,task_session_list" ] || fail "read-only tools/list: $names"
resp "$out" 3 | jq -e '.result.isError==true and (.result.content[0].text|contains("--allow-mutations"))' >/dev/null || fail "task_claim not refused without --allow-mutations"

echo "== with --allow-mutations: exactly the eight task_* tools; claim, comment, finish; a workflow tool refused"
# The server answers each request from its own goroutine, so a request is sent only after the previous
# one was answered (bounded wait), not after a guessed pause.
fifo="$root/in"; outf="$root/out"; mkfifo "$fifo"; : >"$outf"
rpc --allow-mutations <"$fifo" >"$outf" 2>"$root/rpc.err" &
srv=$!
exec 3>"$fifo"
await() { # id : wait up to 20s for the response with this id
  local i
  for i in $(seq 1 200); do
    jq -e "select(.id==$1)" "$outf" >/dev/null 2>&1 && return 0
    sleep 0.1
  done
  fail "no response to request $1 within 20s: $(cat "$root/rpc.err")"
}
echo "$init" >&3; await 1
echo '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' >&3; await 2
call 3 task_claim '{"next":true}' >&3; await 3
call 4 task_comment '{"id":1,"text":"working on it"}' >&3; await 4
call 5 task_finish '{"id":1,"result":"done in the smoke script"}' >&3; await 5
call 6 workflow_list '{}' >&3; await 6
exec 3>&-
wait "$srv" || true
out="$(cat "$outf")"
names="$(resp "$out" 2 | jq -r '[.result.tools[].name]|sort|join(",")')"
[ "$names" = "task_add,task_artifact,task_claim,task_comment,task_finish,task_get,task_list,task_next,task_release,task_session_get,task_session_list,task_split,task_verify" ] || fail "tools/list: $names"
resp "$out" 3 | jq -e '.result.isError!=true and (.result.content[0].text|contains("agent:smoke-client#"))' >/dev/null || fail "task_claim: $(resp "$out" 3)"
resp "$out" 4 | jq -e '.result.isError!=true' >/dev/null || fail "task_comment: $(resp "$out" 4)"
resp "$out" 5 | jq -e '.result.isError!=true' >/dev/null || fail "task_finish: $(resp "$out" 5)"
resp "$out" 6 | jq -e '.result.isError==true and (.result.content[0].text|contains("not served"))' >/dev/null || fail "workflow_list not refused: $(resp "$out" 6)"

echo "== the board as the command shows it: the agent's work waits in review"
run --profile "$prof" --json task show 1 | jq -e '.task.status=="review" and ([.events[].kind]|index("claimed")!=null) and ([.events[].kind]|index("result")!=null)' >/dev/null || fail "task show 1: $(run --profile "$prof" --json task show 1)"

echo "== delegation: off by default, an agent cannot turn it on, task_approve exists only while the operator allows it"
run --profile "$prof" --json task add "captured text" >"$root/inbox.out" 2>"$root/inbox.err" || fail "task add (inbox): $(cat "$root/inbox.out")"
inbox_id="$(jq -r '.task.id' "$root/inbox.out")"
if run --profile "$prof" task agents allow --approve --as bot >/dev/null 2>"$root/err"; then fail "an agent allowed itself to approve"; fi
grep -q "only the operator" "$root/err" || fail "agents allow by an agent: $(cat "$root/err")"
run --profile "$prof" --json task agents show | jq -e '.view==false and .approve==false' >/dev/null || fail "delegation is not off by default"
out="$({ echo "$init"; echo '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'; } | rpc --allow-mutations)"
[ "$(resp "$out" 2 | jq -r '[.result.tools[].name]|sort|join(",")')" = "task_add,task_artifact,task_claim,task_comment,task_finish,task_get,task_list,task_next,task_release,task_session_get,task_session_list,task_split,task_verify" ] || fail "tools/list changed while delegation is off"
# allow needs a terminal on standard input: refused here (no terminal), done below through a pseudo-terminal.
if run --profile "$prof" task agents allow --view --approve </dev/null >/dev/null 2>"$root/err"; then fail "task agents allow ran without a terminal"; fi
grep -q "your own terminal" "$root/err" || fail "allow without a terminal: $(cat "$root/err")"
allow_tty() { env -i "PATH=$PATH" "MONOAGENT_DEV_ENFORCE_FROM=2999-01-01T00:00:00Z" "HOME=$home" "TMPDIR=$root/tmp" python3 -c 'import pty,sys; sys.exit(pty.spawn(sys.argv[1:]) >> 8)' "$cli" --db-path "$db" --profile "$prof" task agents allow "$@" >/dev/null; }
if allow_tty --approve; then fail "allow --approve without --view was accepted"; fi
allow_tty --view --approve || fail "operator: task agents allow --view --approve"
out="$({ echo "$init"; echo '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'; call 3 task_approve "{\"ids\":[$inbox_id]}"; } | rpc --allow-mutations)"
resp "$out" 2 | jq -e '[.result.tools[].name]|index("task_approve")!=null' >/dev/null || fail "task_approve not listed while allowed"
resp "$out" 3 | jq -e '.result.isError!=true' >/dev/null || fail "task_approve: $(resp "$out" 3)"
run --profile "$prof" --json task show "$inbox_id" | jq -e '.task.status=="ready" and (.events[-1].actor|startswith("agent:smoke-client#")) and (.events[-1].note|contains("delegated"))' >/dev/null || fail "the approval is not recorded as delegated: $(run --profile "$prof" --json task show "$inbox_id")"
# the loop is closed: a task an agent added over MCP cannot be approved by an agent
out="$({ echo "$init"; call 2 task_add '{"title":"injected"}'; } | rpc --allow-mutations)"
agent_id="$(resp "$out" 2 | jq -r '.result.content[0].text | fromjson | .task.id')"
out="$({ echo "$init"; call 3 task_approve "{\"ids\":[$agent_id]}"; } | rpc --allow-mutations)"
resp "$out" 3 | jq -e '.result.isError==true and (.result.content[0].text|contains("created by an agent"))' >/dev/null || fail "an agent approved a task an agent created: $(resp "$out" 3)"
run --profile "$prof" task agents deny >/dev/null || fail "operator: task agents deny"
out="$({ echo "$init"; echo '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'; call 3 task_approve '{"ids":[1]}'; } | rpc --allow-mutations)"
resp "$out" 2 | jq -e '[.result.tools[].name]|index("task_approve")==null' >/dev/null || fail "task_approve still listed after deny"
resp "$out" 3 | jq -e '.result.isError==true' >/dev/null || fail "task_approve worked after deny"

echo "== sessions: the operator plans and approves, agents work the session through the tools"
# A session is created and approved only by the operator (never an MCP tool), so it is set up with the CLI.
run --profile "$prof" --json task session new "smoke session" --goal "exercise the session tools" >"$root/s1.out" 2>"$root/s1.err" || fail "task session new: $(cat "$root/s1.out" "$root/s1.err")"
s1="$(jq -r '.session.id' "$root/s1.out")"
run --profile "$prof" --json task session new "other session" >"$root/s2.out" 2>"$root/s2.err" || fail "task session new (other): $(cat "$root/s2.out" "$root/s2.err")"
s2="$(jq -r '.session.id' "$root/s2.out")"
sess_add() { # session title [--depends-on ids]: the id of the task added to the session
  local s="$1" t="$2"; shift 2
  run --profile "$prof" --json task add "$t" --session "$s" --acceptance "done when $t passes" "$@" >"$root/sa.out" 2>"$root/sa.err" || fail "task add --session: $(cat "$root/sa.out" "$root/sa.err")"
  jq -r '.task.id' "$root/sa.out"
}
ta="$(sess_add "$s1" "first step")"
tb="$(sess_add "$s1" "second step" --depends-on "$ta")"
tc="$(sess_add "$s2" "other session step")"
run --profile "$prof" task session approve "$s1" >/dev/null || fail "task session approve $s1"
run --profile "$prof" task session approve "$s2" >/dev/null || fail "task session approve $s2"
personal="$inbox_id" # a task of the personal board

# One long-lived server over named pipes, as above: the worker, which claims, comments, attaches and finishes.
worker_in="$root/w_in"; worker_out="$root/w_out"; mkfifo "$worker_in"; : >"$worker_out"
rpc --allow-mutations <"$worker_in" >"$worker_out" 2>"$root/w.err" &
wsrv=$!
exec 3>"$worker_in"
outf="$worker_out"
echo "$init" >&3; await 1
call 2 task_session_list '{}' >&3; await 2
call 3 task_session_get "{\"id\":$s1}" >&3; await 3
call 4 task_next "{\"session\":$s1}" >&3; await 4
call 5 task_claim "{\"id\":$tb,\"session\":$s1}" >&3; await 5
call 6 task_claim "{\"id\":$tc,\"session\":$s1}" >&3; await 6
call 7 task_claim "{\"next\":true,\"session\":$s1}" >&3; await 7
call 8 task_next "{\"session\":$s1}" >&3; await 8
call 9 task_comment "{\"id\":$ta,\"session\":$s1,\"text\":\"halfway\"}" >&3; await 9
call 10 task_artifact "{\"id\":$ta,\"kind\":\"commit\",\"ref\":\"abc123\",\"note\":\"the change\"}" >&3; await 10
call 11 task_finish "{\"id\":$ta,\"session\":$s1,\"result\":\"done\"}" >&3; await 11
call 12 task_verify "{\"id\":$ta,\"pass\":true,\"note\":\"my own work\"}" >&3; await 12
exec 3>&-
wait "$wsrv" || true
out="$(cat "$worker_out")"
resp "$out" 2 | jq -e --argjson a "$s1" --argjson b "$s2" '.result.isError!=true and ((.result.content[0].text|fromjson|.sessions|map(.id))|index($a)!=null and index($b)!=null)' >/dev/null || fail "task_session_list: $(resp "$out" 2)"
resp "$out" 3 | jq -e --argjson a "$ta" --argjson b "$tb" '.result.isError!=true and ((.result.content[0].text|fromjson|.tasks|map(.id))|sort)==([$a,$b]|sort)' >/dev/null || fail "task_session_get: $(resp "$out" 3)"
resp "$out" 4 | jq -e --argjson a "$ta" '.result.isError!=true and (.result.content[0].text|fromjson|.task.id)==$a' >/dev/null || fail "task_next in the session is not the first step: $(resp "$out" 4)"
resp "$out" 5 | jq -e '.result.isError==true' >/dev/null || fail "claimed a task that waits for its dependency: $(resp "$out" 5)"
resp "$out" 6 | jq -e '.result.isError==true' >/dev/null || fail "claimed a task of another session through this one: $(resp "$out" 6)"
resp "$out" 7 | jq -e --argjson a "$ta" '.result.isError!=true and (.result.content[0].text|fromjson|.task|.id==$a and .status=="in_progress")' >/dev/null || fail "task_claim next in the session: $(resp "$out" 7)"
resp "$out" 8 | jq -e '.result.isError!=true and (.result.content[0].text|fromjson|.task)==null' >/dev/null || fail "the second step is offered before the first is done: $(resp "$out" 8)"
resp "$out" 9 | jq -e '.result.isError!=true' >/dev/null || fail "task_comment (session): $(resp "$out" 9)"
resp "$out" 10 | jq -e '.result.isError!=true and (.result.content[0].text|fromjson|.artifact.kind)=="commit"' >/dev/null || fail "task_artifact: $(resp "$out" 10)"
resp "$out" 11 | jq -e '.result.isError!=true and (.result.content[0].text|fromjson|.task.status)=="review"' >/dev/null || fail "task_finish (session): $(resp "$out" 11)"
resp "$out" 12 | jq -e '.result.isError==true and (.result.content[0].text|contains("invalid_input"))' >/dev/null || fail "the agent that finished a task verified it: $(resp "$out" 12)"

echo "== a different agent verifies: a pass marks it verified, closing it stays the operator's"
checker_init='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"smoke checker","version":"1"}}}'
out="$({ echo "$checker_init"; call 2 task_verify "{\"id\":$ta,\"pass\":true,\"note\":\"meets the acceptance\"}"; } | rpc --allow-mutations)"
resp "$out" 2 | jq -e '.result.isError!=true and (.result.content[0].text|fromjson|.task|.verified==true and .status=="review")' >/dev/null || fail "another agent's task_verify: $(resp "$out" 2)"

echo "== a server bound to one session reaches nothing outside it"
# MONOAGENT_TASK_SESSION is what a session worker's token carries; run() starts from a clean environment, so add it here.
rpc_bound() { mono_guard env -i "PATH=$PATH" "MONOAGENT_DEV_ENFORCE_FROM=2999-01-01T00:00:00Z" "HOME=$home" "TMPDIR=$root/tmp" "MONOAGENT_TASK_SESSION=$s1" "$cli" --db-path "$db" mcp --tasks-only --allow-mutations; }
out="$({ echo "$init"
  call 2 task_session_get "{\"id\":$s1}"
  call 3 task_session_get "{\"id\":$s2}"
  call 4 task_claim "{\"next\":true,\"session\":$s2}"
  call 5 task_claim "{\"id\":$tc}"
  call 6 task_get "{\"id\":$personal}"
  call 7 task_list '{}'
  call 8 task_add '{"title":"on the personal board"}'
  call 9 task_claim '{"next":true}'; } | rpc_bound)"
resp "$out" 2 | jq -e '.result.isError!=true' >/dev/null || fail "the bound session is not served: $(resp "$out" 2)"
for id in 3 4 5 6 7 8 9; do
  resp "$out" "$id" | jq -e '.result.isError==true' >/dev/null || fail "a server bound to session $s1 reached outside it (request $id): $(resp "$out" "$id")"
done
run --profile "$prof" --json task show "$tc" | jq -e '.task.status=="ready"' >/dev/null || fail "the other session's task moved"

echo "== --tasks-only with --api-only is refused"
if run mcp --tasks-only --api-only </dev/null >/dev/null 2>"$root/err"; then fail "--tasks-only --api-only started"; fi
grep -q "cannot be combined" "$root/err" || fail "refusal text: $(cat "$root/err")"

mono_check_clean
echo "tasks mcp smoke: ok"
