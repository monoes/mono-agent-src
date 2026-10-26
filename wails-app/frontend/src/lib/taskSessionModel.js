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
    case 'running': return ['pause', 'stop', 'budget']
    case 'paused': return ['resume', 'stop', 'budget']
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
