// The person item bindings (wails-app/app_person_items.go). Like sessionsApi, each call
// resolves to the CLI's JSON (`monoagentcli task session person …`) or to {error, code}
// and never rejects. Who may answer and what each answer does are the CLI's rules.
import * as GoApp from '../wailsjs/go/main/App'

const run = (call) => Promise.resolve()
  .then(call)
  .then(r => (typeof r === 'string' ? JSON.parse(r) : r))
  .catch(e => ({ error: e?.message || String(e) }))

export const personItemsApi = {
  // {profile, items: [{id, kind, sessionId, conversationId, title, goal, reasons, taskCount, budgetUsd, budgetShare, …}]}
  list: () => run(() => GoApp.PersonItemsList()),
  // {profile, session}; rev and digest are the ones the list showed: a plan that changed since
  // is refused with code 'plan_changed'.
  approve: (sessionId, rev, digest) => run(() => GoApp.PersonItemApprove(sessionId, rev, digest || '')),
  // {profile, session}: the session stays a draft; note goes to its lead.
  deny: (sessionId, note) => run(() => GoApp.PersonItemDeny(sessionId, note)),
  // {profile, sessionId, commandId, item: 'open'}: the question goes to the lead; the item stays open.
  ask: (sessionId, text) => run(() => GoApp.PersonItemAsk(sessionId, text)),
}
