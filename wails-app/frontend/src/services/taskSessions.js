// The task session bindings (wails-app/app_task_sessions.go). Like tasksApi,
// each call resolves to the CLI's JSON (`monoagentcli task session … --json`)
// or to {error, code} and never rejects; the page shows a refusal where the
// action was taken. No rule lives here: what an operator may do is the CLI's.
import * as GoApp from '../wailsjs/go/main/App'

// The Done column shows the newest cards, as the personal board does (tasks.js DONE_LIMIT).
const DONE_LIMIT = 50

const run = (call) => Promise.resolve()
  .then(call)
  .then(r => (typeof r === 'string' ? JSON.parse(r) : r))
  .catch(e => ({ error: e?.message || String(e) }))

export const sessionsApi = {
  // {profile, sessions: [...]}; all includes archived sessions.
  list:    (all = false) => run(() => GoApp.TaskSessions(all)),
  // {profile, session, events}
  show:    (id, events = 30) => run(() => GoApp.TaskSessionShow(id, events)),
  // {profile, session, tasks: [...]} with depends_on and acceptance.
  plan:    (id) => run(() => GoApp.TaskSessionPlan(id)),
  // {profile, session, rev, counts, tasks: {inbox: [...], ...}}
  board:   (id) => run(() => GoApp.TaskSessionBoard(id, DONE_LIMIT)),
  approve: (id) => run(() => GoApp.TaskSessionApprove(id)),
  pause:   (id) => run(() => GoApp.TaskSessionPause(id)),
  resume:  (id) => run(() => GoApp.TaskSessionResume(id)),
  stop:    (id) => run(() => GoApp.TaskSessionStop(id)),
  archive: (id) => run(() => GoApp.TaskSessionArchive(id)),
  // budget in dollars; 0 for none.
  budget:  (id, usd) => run(() => GoApp.TaskSessionBudget(id, JSON.stringify({ usd }))),
  // {session_id, rev, state}, or {} for a session that is not there.
  pulse:   (id) => run(() => GoApp.TaskSessionPulse(id)),
}
