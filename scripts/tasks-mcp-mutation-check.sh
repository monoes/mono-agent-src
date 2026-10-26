#!/usr/bin/env bash
# Mutation proof for the task MCP tools (issue #371, plan Task 6 step 3): removes, one at a time, the
# mutation gate, the --tasks-only filter and the per-profile scoping, and requires the internal/mcp
# tests to fail for each. A mutation that survives means a rule is no longer tested. Edits are made
# to a scratch copy of the repository, never the working tree.
#
#   scripts/tasks-mcp-mutation-check.sh
set -euo pipefail

src="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d)"
trap 'rm -r "$work" 2>/dev/null || true' EXIT
cp -r "$src/." "$work/"
cd "$work"

testcmd=(go test ./internal/mcp -run 'Task|Tasks|Grant' -count=1)

# The unmutated tree must pass first: otherwise every mutation would look "killed" for the wrong reason.
if ! "${testcmd[@]}" >"$work/base.txt" 2>&1; then
  cat "$work/base.txt" >&2
  echo "mutation check: the unmutated tests fail, so no mutation result would mean anything; fix them first" >&2
  exit 2
fi

survived=0
mutate() { # name file old new
  local name="$1" file="$2" old="$3" new="$4"
  cp "$file" "$file.orig"
  OLD="$old" NEW="$new" python3 - "$file" <<'PY'
import os, sys
f = sys.argv[1]
s = open(f).read()
old = os.environ["OLD"]
if old not in s:
    sys.exit("anchor missing in %s: %s" % (f, old))
open(f, "w").write(s.replace(old, os.environ["NEW"], 1))
PY
  if "${testcmd[@]}" >"$work/out.txt" 2>&1; then
    echo "SURVIVED: $name"
    survived=1
  else
    echo "killed:   $name"
  fi
  mv "$file.orig" "$file"
}

mutate "mutating tools callable without --allow-mutations" internal/mcp/tools.go 't.mutating && !s.opts.AllowMutations {' 'false {'
mutate "mutating tools listed without --allow-mutations" internal/mcp/tools.go 't.mutating && !allowMutations {' 'false {'
mutate "--tasks-only no longer narrows the server" internal/mcp/apionly.go 'case s.opts.TasksOnly:' 'case false:'
mutate "--tasks-only serves every tool" internal/mcp/apionly.go 'keep = taskToolNames()' 'return all'
mutate "a call follows another profile" internal/mcp/task_tools.go 'store.Profile(ctx, profileOf(ctx))' 'store.Profile(ctx, "p-7f3a9c")'
mutate "task_approve served although no operator allowed it" internal/mcp/task_approve.go 'err == nil && a.Approve && a.View {' 'true {'
mutate "task_approve served without view" internal/mcp/task_approve.go 'a.Approve && a.View {' 'a.Approve {'
mutate "a delegate approves a task an agent created" internal/tasks/ops.go 'return s.refuseAgentCreated(ctx, cur)' 'return nil'
mutate "a delegate approves without a cap" internal/tasks/ops.go 'if len(ids) > MaxDelegatedApprove {' 'if false {'
mutate "task_approve served without asking the setting" internal/mcp/apionly.go 'if s.approveDelegated() {' 'if true {'
mutate "a scoped tool is called without resolving its profile" internal/mcp/tools.go 'if !t.global {' 'if false {'
mutate "a missing profile argument is let through" internal/mcp/profile_arg.go 'if !present {' 'if false {'
mutate "a grant server accepts another profile" internal/mcp/grant.go 'if id != b.ProfileID {' 'if false {'
# Every tool must scope to the profile: the profile id is replaced by "" in each store call.
scope() { # tool file call-prefix
  mutate "$1 ignores the profile" "internal/mcp/$2" "$3(ctx, p.ID," "$3(ctx, \"\","
}
# The session-aware tools reach the store through a helper (claimTask, commentTask, ...) that takes the
# profile after the store: the same replacement, on that call.
helper() { # tool file helper-name
  mutate "$1 ignores the profile" "internal/mcp/$2" "$3(ctx, store, p.ID," "$3(ctx, store, \"\","
}
# One line per tool and per store function it calls; the anchor must occur in the file (mutate stops the
# script otherwise) and only its first occurrence is replaced.
scope task_list task_read.go store.List
scope "task_list (session)" task_read.go store.SessionTasks
scope task_get task_read.go store.Get
scope "task_get (session)" task_read.go store.SessionTask
scope task_next task_read.go store.Next
scope "task_next (session)" task_read.go store.SessionNext
scope "task_claim (next)" task_write.go store.Next
scope "task_claim (next, session)" task_write.go store.SessionNext
helper task_claim task_write.go claimTask
helper task_comment task_write.go commentTask
helper task_finish task_write.go finishTask
helper task_release task_write.go releaseTask
scope task_add task_write.go store.Add
scope "task_add (session)" task_write.go store.AddSessionTask
scope task_approve task_approve.go store.Approve
scope task_session_list task_session.go store.ListSessions
scope task_session_get task_session.go store.PlanOfSession
scope "task_session_get (events)" task_session.go store.SessionEvents
scope "task_split (session of task)" task_session.go store.SessionOfTask
scope task_split task_session.go store.SplitTask
scope task_verify task_session.go store.VerifySessionTask
scope task_artifact task_session.go store.AddArtifact

exit "$survived"
