// Pure logic of the session half of the Tasks tab (plan §5): what a session
// shows (state chip, progress, spend), which header actions it offers, the
// hints on a card, and the plan as a tree. No React, no DOM, no rules of the
// store: the CLI decides what is allowed; this only decides what to show.

export const SESSION_STATES = ['draft', 'running', 'paused', 'done', 'stopped', 'failed']

// chipState is the state as a chip class suffix; an unknown state reads as draft.
export const chipState = (s) => (SESSION_STATES.includes(s?.state) ? s.state : 'draft')

// progress is "done/total" of a session's tasks.
export function progress(s) {
  const c = s?.counts || {}
  return `${c.done ?? 0}/${c.total ?? 0}`
}

const usd = (n) => `$${(Number(n) || 0).toFixed(2)}`

// spendText is "$1.20 of $5.00", or "$1.20" with no budget set.
export function spendParts(s) {
  return { spent: usd(s?.spent_usd), budget: s?.budget_usd > 0 ? usd(s.budget_usd) : '' }
}

// overBudget: 90 percent of a set budget spent (the engine stops there too).
export const nearBudget = (s) => s?.budget_usd > 0 && s.spent_usd >= 0.9 * s.budget_usd

// budgetLevel: 'over' once a set budget is spent, 'near' from 90 percent, else ''.
export const budgetLevel = (s) => (!(s?.budget_usd > 0) ? '' : s.spent_usd >= s.budget_usd ? 'over' : nearBudget(s) ? 'near' : '')

// actionsFor lists the header actions a session offers in its state. The CLI
// refuses what the state does not allow; the button is just not shown.
export function actionsFor(s) {
  if (!s) return []
  if (s.archived) return []
  switch (s.state) {
    case 'draft': return ['approve', 'stop', 'archive', 'budget']
    case 'running': return ['pause', 'close', 'stop', 'budget']
    case 'paused': return ['resume', 'close', 'stop', 'budget']
    default: return ['archive', 'budget'] // done, stopped, failed
  }
}

// Only a running session's board takes drags and key moves.
export const boardEditable = (s) => !s || s.state === 'running'

// awaitingApproval counts the draft sessions the operator has not approved.
export const awaitingApproval = (sessions) => (sessions || []).filter(s => s.state === 'draft' && !s.archived).length

// waitingOn lists the dependencies of task that are not done, from the board.
export function waitingOn(task, board) {
  if (!task?.depends_on?.length || !board) return []
  const done = new Set((board.columns?.done || []).map(t => t.id))
  const known = new Set()
  for (const col of Object.values(board.columns || {})) for (const t of col) known.add(t.id)
  // A dependency that is not on the board (Done is cut to its newest cards) is taken as done.
  return task.depends_on.filter(id => known.has(id) && !done.has(id))
}

// planTree turns the plan's flat tasks into [{task, children: [...]}], roots
// in the plan's order; a parent that is not in the list makes its child a root.
export function planTree(tasks) {
  const nodes = new Map((tasks || []).map(t => [t.id, { task: t, children: [] }]))
  const roots = []
  for (const t of tasks || []) {
    const parent = t.parent_id && nodes.get(t.parent_id)
    if (parent && parent !== nodes.get(t.id)) parent.children.push(nodes.get(t.id))
    else roots.push(nodes.get(t.id))
  }
  return roots
}

// selectionAfter keeps the chosen session while it is still listed; else null (Personal).
export const selectionAfter = (id, sessions) => (id && (sessions || []).some(s => s.id === id) ? id : null)

// MAX_TITLE_RUNES is the longest session title the store takes (tasks.MaxTitleRunes).
export const MAX_TITLE_RUNES = 200

// titleProblem says why a title is refused before the CLI is asked: 'empty' or
// 'tooLong' (counted in characters, as the store does); '' when it is fine.
// The page tells the user; it never cuts a title for them.
export function titleProblem(title) {
  const t = String(title ?? '').trim()
  if (!t) return 'empty'
  return [...t].length > MAX_TITLE_RUNES ? 'tooLong' : ''
}

// sessionTree builds [{session, depth, children}] from the flat list by
// parent_session_id, roots in the list's order. A session whose parent is not in
// the list (gone, archived) is a root; so is the first of any ring of parents,
// so the walk always ends. The list is not changed.
export function sessionTree(sessions) {
  const list = sessions || []
  const byId = new Map(list.map(s => [s.id, s]))
  const cut = new Set() // sessions whose parent link is ignored
  const parentOf = (s) => {
    const p = s.parent_session_id
    return p && p !== s.id && byId.has(p) && !cut.has(s.id) ? p : null
  }
  for (const s of list) {
    const seen = new Set([s.id])
    for (let p = parentOf(s); p; p = parentOf(byId.get(p))) {
      if (seen.has(p)) { cut.add(s.id); break }
      seen.add(p)
    }
  }
  const nodes = new Map(list.map(s => [s.id, { session: s, depth: 1, children: [] }]))
  const roots = []
  for (const s of list) {
    const p = parentOf(s)
    if (p) nodes.get(p).children.push(nodes.get(s.id))
    else roots.push(nodes.get(s.id))
  }
  const setDepth = (n, d) => { n.depth = d; n.children.forEach(c => setDepth(c, d + 1)) }
  roots.forEach(r => setDepth(r, 1))
  return roots
}

// belowOf lists the sessions below session id (not archived), nearest first. A ring of
// parents ends the walk, so it always ends.
export function belowOf(sessions, id) {
  const list = (sessions || []).filter(s => s && !s.archived)
  const seen = new Set([id])
  const out = []
  for (let frontier = [id]; frontier.length;) {
    const next = list.filter(s => frontier.includes(s.parent_session_id) && !seen.has(s.id))
    next.forEach(s => { seen.add(s.id); out.push(s) })
    frontier = next.map(s => s.id)
  }
  return out
}

// openBelow: the sessions below id that are still draft, running or paused (a plain archive is refused for them).
export const openBelow = (sessions, id) => belowOf(sessions, id).filter(s => ['draft', 'running', 'paused'].includes(s.state))

// hasSessionTree: some listed session has a listed parent, so the strip needs the tree.
export const hasSessionTree = (sessions) => sessionTree(sessions).some(r => r.children.length > 0)

// visibleRows flattens the tree into the rows on screen, skipping the children of
// collapsed sessions: [{session, depth, hasChildren, expanded, parentId}].
export function visibleRows(roots, collapsed) {
  const rows = []
  const walk = (n, parentId) => {
    const hasChildren = n.children.length > 0
    const expanded = hasChildren && !collapsed?.has(n.session.id)
    rows.push({ session: n.session, depth: n.depth, hasChildren, expanded, parentId })
    if (expanded) n.children.forEach(c => walk(c, n.session.id))
  }
  roots.forEach(r => walk(r, null))
  return rows
}

// SESSION_PAGE is how many session tabs (or tree rows) the strip draws at first, and how many more each
// "show more" adds.
export const SESSION_PAGE = 100

// capSessionRows keeps the first `limit` of items, and enough more to include the one with id keepId (the
// chosen session is always on screen): {kept, hidden}.
export function capSessionRows(items, limit, keepId, idOf = (x) => x.id) {
  const at = keepId == null ? -1 : items.findIndex(x => idOf(x) === keepId)
  const end = Math.max(limit, at + 1)
  return { kept: items.slice(0, end), hidden: Math.max(items.length - end, 0) }
}
