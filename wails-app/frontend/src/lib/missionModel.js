// Pure logic of the mission cards and strip in a coder chat (plan section 9):
// which events become cards, which session belongs to a conversation, and
// what the strip shows. No React; the CLI decides what is allowed.

const TERMINAL = ['done', 'stopped', 'failed']

// isTerminal: a session that is over (or archived) gets no strip of its own.
export const isTerminal = (s) => !!s && (s.archived || TERMINAL.includes(s.state))

// missionItems turns a conversation's turns ([{key, state}], oldest first)
// into the cards each turn shows: every proposal, the latest plan per
// session and the latest report per session (a later event, in any turn,
// replaces an earlier one and its card). Returns {key: [{type, id, payload}]}.
export function missionItems(turns) {
  const latest = new Map() // `${type}:${sessionId}` -> turn key
  turns.forEach(({ key, state }) => {
    for (const e of state?.missions || []) {
      if (e.type !== 'mission.propose' && e.payload?.sessionId != null) latest.set(`${e.type}:${e.payload.sessionId}`, key)
    }
  })
  const out = {}
  turns.forEach(({ key, state }) => {
    const items = []
    const seen = new Set()
    const list = state?.missions || []
    list.forEach((e, i) => {
      const p = e.payload || {}
      if (e.type === 'mission.propose') {
        items.push({ type: 'propose', id: `${key}:${i}`, payload: p, at: e.at })
        return
      }
      if (p.sessionId == null) return
      const sig = `${e.type}:${p.sessionId}`
      if (latest.get(sig) !== key || seen.has(sig)) return
      // The last event of this turn for the session is the one shown.
      const last = list.map((x, j) => (`${x.type}:${x.payload?.sessionId}` === sig ? j : -1)).filter(j => j >= 0).pop()
      if (last !== i) return
      seen.add(sig)
      items.push({ type: e.type === 'mission.plan' ? 'plan' : 'report', id: sig, payload: p })
    })
    if (items.length) out[key] = items
  })
  return out
}

// startedProposals: the ids of the proposals that were taken up. items is
// the flat list of cards in transcript order; a proposal is started when it
// was confirmed in this view, when a plan card follows it, or when this
// conversation has an open session created after it. A later, separate
// proposal is therefore still offered.
export function startedProposals(items, confirmed, sessions, conversationId) {
  const out = new Set()
  const open = (sessions || []).filter(s => s.conversation_id === conversationId && !isTerminal(s))
  items.forEach((it, i) => {
    if (it.type !== 'propose') return
    const at = Date.parse(it.at)
    if (confirmed?.has(it.id) || items.slice(i + 1).some(x => x.type === 'plan')
      || (!Number.isNaN(at) && open.some(s => Date.parse(s.created_at) > at))) out.add(it.id)
  })
  return out
}

// sessionOf is the session of a conversation: the newest one still going,
// else the newest one that is not archived (a finished mission), else null.
export function sessionOf(sessions, conversationId) {
  if (!conversationId) return null
  const mine = (sessions || []).filter(s => s.conversation_id === conversationId && !s.archived)
  mine.sort((a, b) => b.id - a.id)
  return mine.find(s => !isTerminal(s)) || mine[0] || null
}

// stripFor says whether the strip shows for a session: while it is not over,
// and a finished one once its report arrived (the strip then reads Done).
export const stripFor = (session, reported) => !!session && (!isTerminal(session) || (session.state === 'done' && !!reported))

// workingOn lists the tasks in progress on a session's board: [{id, title, worker}].
export function workingOn(board) {
  const col = board?.tasks?.in_progress || board?.columns?.in_progress || []
  return col.map(t => ({ id: t.id, title: t.title, worker: t.assignee || t.claim?.by || '' }))
}

// stripActions: the buttons the strip offers (the CLI refuses the rest anyway).
export function stripActions(session) {
  if (!session || isTerminal(session)) return []
  if (session.state === 'running') return ['pause', 'stop']
  if (session.state === 'paused') return ['resume', 'stop']
  return ['stop'] // draft
}

// The final report is attached by the backend as an artifact (ref
// final-report) on the session's Integration check task (kind check, title
// starting with Integration). The artifact fields may come as ref or as
// ref_untrusted (the CLI's view of worker text).
export const integrationTasks = (tasks) => (tasks || []).filter(t => t.kind === 'check' && /^integration/i.test(t.title || ''))

export const hasFinalReport = (artifacts) => (artifacts || []).some(a => a.kind === 'final-report' || (a.ref ?? a.ref_untrusted) === 'final-report')

// pickReportTask chooses the task whose drawer shows the report:
// the task with a final-report artifact (artifactsByTask: {taskId: [artifact]}),
// else the first Integration check task, else null (open the session only).
export function pickReportTask(tasks, artifactsByTask) {
  const by = artifactsByTask || {}
  const withReport = (tasks || []).find(t => hasFinalReport(by[t.id]))
  if (withReport) return { taskId: withReport.id, hasReport: true }
  const integ = integrationTasks(tasks)[0]
  return integ ? { taskId: integ.id, hasReport: false } : null
}
