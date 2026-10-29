#!/usr/bin/env bash
# Smoke test for `monoagentcli mcp --tasks-only` over stdio, against a throwaway
# HOME and database: initialize, tools/list (exactly the task_* tools), the
# mutating verbs gated by --allow-mutations, claim -> comment -> finish, a
# task session worked through the tools (dependencies, evidence, no self-verification, an environment label that binds
# nothing), a non-task tool refused by name, the CLI showing what the agent did, and the
# --api-only combination refused, and the session verbs of the command line (list, show, plan, depend, undepend,
# split, budget, pause, resume, close, archive, stop) with the agent-versus-operator gates. Never touches the real ~/.monoagent.
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
# The evidence rule checks a commit against git: the worker's server runs in a scratch repository with one commit.
mkdir -p "$root/repo" && git -C "$root/repo" init -q && git -C "$root/repo" -c user.name=smoke -c user.email=smoke@example.invalid commit -q --allow-empty -m "smoke" || fail "scratch repository"
commit="$(git -C "$root/repo" rev-parse HEAD)"
(cd "$root/repo" && rpc --allow-mutations) <"$worker_in" >"$worker_out" 2>"$root/w.err" &
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
call 10 task_artifact "{\"id\":$ta,\"kind\":\"commit\",\"ref\":\"$commit\",\"note\":\"the change\"}" >&3; await 10
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

echo "== session verbs from the command line: list, show, plan, depend, undepend, split, budget, pause, resume, close, archive, stop"
# Run here as the operator (a clean environment, as above). The operator's own verbs (undepend, budget, resume, close,
# archive) are also tried as an agent (--as) and must be refused and change nothing; depend, split, pause and stop are
# open to agents and are tried as one. This is a CI-only check: the operator steps cannot run from an agent's shell.
sj() { run --profile "$prof" --json "$@"; }
sj task session list >"$root/sl.out" 2>"$root/sl.err" || fail "task session list: $(cat "$root/sl.err")"
jq -e --argjson a "$s1" --argjson b "$s2" '[.sessions[]|select(.id==$a or .id==$b)|.state]==["running","running"]' "$root/sl.out" >/dev/null || fail "task session list: $(cat "$root/sl.out")"
sj task session list --state draft >"$root/sl.out" 2>"$root/sl.err" || fail "task session list --state draft: $(cat "$root/sl.err")"
jq -e --argjson a "$s1" --argjson b "$s2" '(.sessions|map(.id)) as $ids | ($ids|index($a))==null and ($ids|index($b))==null' "$root/sl.out" >/dev/null || fail "a running session listed as a draft: $(cat "$root/sl.out")"
sj task session show "$s1" >"$root/ss.out" 2>"$root/ss.err" || fail "task session show: $(cat "$root/ss.err")"
jq -e --argjson a "$s1" '.session.id==$a and .session.state=="running" and .session.title=="smoke session" and (.events|length)>0' "$root/ss.out" >/dev/null || fail "task session show: $(cat "$root/ss.out")"

# A session of its own for the verbs that change things, planned as a draft.
sj task session new "verbs session" --goal "exercise the session verbs" >"$root/s3.out" 2>"$root/s3.err" || fail "task session new (verbs): $(cat "$root/s3.out" "$root/s3.err")"
s3="$(jq -r '.session.id' "$root/s3.out")"
t1="$(sess_add "$s3" "step one")"
t2="$(sess_add "$s3" "step two")"
t3="$(sess_add "$s3" "step three")"
sj task depend "$t2" --on "$t1" >"$root/d.out" 2>"$root/d.err" || fail "task depend: $(cat "$root/d.err")"
jq -e --argjson t "$t2" --argjson d "$t1" '.task.id==$t and (.task.depends_on|index($d))!=null' "$root/d.out" >/dev/null || fail "task depend: $(cat "$root/d.out")"
if sj task depend "$t1" --on "$t2" >/dev/null 2>"$root/err"; then fail "a dependency cycle was accepted"; fi
grep -q "cycle" "$root/err" || fail "the cycle refusal: $(cat "$root/err")"
sj task session plan "$s3" >"$root/p.out" 2>"$root/p.err" || fail "task session plan: $(cat "$root/p.err")"
jq -e --argjson a "$t1" --argjson b "$t2" --argjson c "$t3" --argjson d "$t1" '([.tasks[].id]|sort)==([$a,$b,$c]|sort) and ([.tasks[]|select(.id==$b)][0].depends_on|index($d))!=null' "$root/p.out" >/dev/null || fail "task session plan: $(cat "$root/p.out")"
if run --profile "$prof" task undepend "$t2" "$t1" --as bot >/dev/null 2>&1; then fail "an agent removed a dependency"; fi
sj task session plan "$s3" | jq -e --argjson b "$t2" --argjson d "$t1" '[.tasks[]|select(.id==$b)][0].depends_on|index($d)!=null' >/dev/null || fail "the dependency is gone after an agent's undepend"
sj task undepend "$t2" "$t1" >"$root/d.out" 2>"$root/d.err" || fail "task undepend: $(cat "$root/d.err")"
jq -e --argjson d "$t1" '((.task.depends_on // [])|index($d))==null' "$root/d.out" >/dev/null || fail "task undepend: $(cat "$root/d.out")"
sj task split "$t3" --into "part a|accept a" --into "part b|accept b" >"$root/sp.out" 2>"$root/sp.err" || fail "task split: $(cat "$root/sp.err")"
jq -e '(.tasks|map(.title)|sort)==["part a","part b"]' "$root/sp.out" >/dev/null || fail "task split: $(cat "$root/sp.out")"

run --profile "$prof" task session approve "$s3" >/dev/null || fail "task session approve $s3"
sj task session budget "$s3" --usd 5 --max-tasks 150 >"$root/b.out" 2>"$root/b.err" || fail "task session budget: $(cat "$root/b.err")"
jq -e '.session.budget_usd==5 and .session.max_tasks==150' "$root/b.out" >/dev/null || fail "task session budget: $(cat "$root/b.out")"
sj task session budget "$s3" --spend 1.25 --note "smoke" >"$root/b.out" 2>"$root/b.err" || fail "task session budget --spend: $(cat "$root/b.err")"
jq -e '.session.spent_usd==1.25 and .session.budget_usd==5' "$root/b.out" >/dev/null || fail "task session budget --spend: $(cat "$root/b.out")"
if sj task session budget "$s3" >/dev/null 2>&1; then fail "task session budget with nothing to change was accepted"; fi
if run --profile "$prof" task session budget "$s3" --usd 99 --as bot >/dev/null 2>&1; then fail "an agent raised a budget"; fi
sj task session show "$s3" | jq -e '.session.budget_usd==5' >/dev/null || fail "an agent changed the budget"

echo "-- pause is open to an agent, resume and close are not, archive waits for the session to end"
run --profile "$prof" --json task session pause "$s3" --as bot | jq -e '.session.state=="paused"' >/dev/null || fail "an agent could not pause a session"
if run --profile "$prof" task session resume "$s3" --as bot >/dev/null 2>&1; then fail "an agent resumed a session"; fi
sj task session show "$s3" | jq -e '.session.state=="paused"' >/dev/null || fail "the session is not paused after an agent's resume"
sj task session resume "$s3" | jq -e '.session.state=="running"' >/dev/null || fail "task session resume"
if run --profile "$prof" task session close "$s3" --as bot >/dev/null 2>&1; then fail "an agent closed a session"; fi
if sj task session archive "$s3" >/dev/null 2>&1; then fail "a running session was archived"; fi
sj task session close "$s3" --note "smoke done" | jq -e '.session.state=="done"' >/dev/null || fail "task session close"
if run --profile "$prof" task session archive "$s3" --as bot >/dev/null 2>&1; then fail "an agent archived a session"; fi
sj task session archive "$s3" | jq -e '.session.archived==true' >/dev/null || fail "task session archive"
sj task session list | jq -e --argjson a "$s3" '(.sessions|map(.id)|index($a))==null' >/dev/null || fail "an archived session is in the default listing"
sj task session list --all | jq -e --argjson a "$s3" '(.sessions|map(.id)|index($a))!=null' >/dev/null || fail "an archived session is missing from the listing with --all"

echo "-- stop is open to an agent, and a stopped session does not run again"
sj task session new "stopped session" >"$root/s4.out" 2>"$root/s4.err" || fail "task session new (stopped): $(cat "$root/s4.out" "$root/s4.err")"
s4="$(jq -r '.session.id' "$root/s4.out")"
run --profile "$prof" --json task session stop "$s4" --as bot | jq -e '.session.state=="stopped"' >/dev/null || fail "an agent could not stop a session"
if sj task session resume "$s4" >/dev/null 2>&1; then fail "a stopped session was resumed"; fi

echo "== an environment label binds nothing: a server given MONOAGENT_TASK_SESSION but no token is an ordinary agent"
# What binds a server to a session, a task and a role is a mission token the conductor mints per run
# (MONOAGENT_TASK_BINDING); the MONOAGENT_TASK_* labels scope nothing, so a server that carries only one
# reads other sessions and the personal board as any unbound agent does. The bound-token cases (a worker
# held to its task, a verifier, a token that does not verify, an unbound agent in a managed session) need
# a minted token and are covered by Go tests: internal/mcp TestWorkerTokenBindsTheServerToItsTask,
# TestEnvironmentLabelsBindNothing, TestMissionTokenThatDoesNotVerifyRefusesEveryAct,
# TestOnlyTheBoundVerifierTokenVerifies; internal/cli/task TestLabelsInTheEnvironmentBindNothing,
# TestMissionTokenCases; internal/tasks TestUnboundAgentCannotChangeWorkInAManagedSession.
# run() starts from a clean environment, so the label is added here. Reads only: nothing moves.
rpc_labelled() { mono_guard env -i "PATH=$PATH" "MONOAGENT_DEV_ENFORCE_FROM=2999-01-01T00:00:00Z" "HOME=$home" "TMPDIR=$root/tmp" "MONOAGENT_TASK_SESSION=$s1" "$cli" --db-path "$db" mcp --tasks-only --allow-mutations; }
out="$({ echo "$init"
  call 2 task_session_get "{\"id\":$s1}"
  call 3 task_session_get "{\"id\":$s2}"
  call 4 task_get "{\"id\":$personal}"; } | rpc_labelled)"
for id in 2 3 4; do
  resp "$out" "$id" | jq -e '.result.isError!=true' >/dev/null || fail "an environment label scoped a server without a token (request $id): $(resp "$out" "$id")"
done
run --profile "$prof" --json task show "$tc" | jq -e '.task.status=="ready"' >/dev/null || fail "the other session's task moved"

echo "== --tasks-only with --api-only is refused"
if run mcp --tasks-only --api-only </dev/null >/dev/null 2>"$root/err"; then fail "--tasks-only --api-only started"; fi
grep -q "cannot be combined" "$root/err" || fail "refusal text: $(cat "$root/err")"

mono_check_clean
echo "tasks mcp smoke: ok"
