// Pure model of a mission's agent orbit: the task sessions below one session, as the small circles that orbit
// its bubble, and the geometry that places them. No React, no DOM.
//
// Input is the flat list of task sessions (`task session list`): id, parent_session_id,
// conversation_id, title, state, spent_usd, counts. Anything odd (an unknown state,
// a parent that is gone, a ring of parents) is read as the plainest thing, never thrown.

import { sessionTree } from './taskSessionModel.js'

export const MAX_SATELLITES = 8
export const MAX_LEVEL = 4

// The ring states, most urgent first. A person waiting or a failure is never hidden
// in the "+N" chip; sort() keeps the list order inside one state.
export const ORBIT_STATES = ['waiting', 'failed', 'running', 'paused', 'stopped', 'idle', 'done']
const URGENCY = Object.fromEntries(ORBIT_STATES.map((s, i) => [s, i]))

// orbitState reads a session as one of ORBIT_STATES. A draft is waiting for the
// person's approval; a stopped session was ended on purpose before it finished: its own neutral state, not a failure.
export function orbitState(session) {
  if (Number(session?.waiting) > 0) return 'waiting'
  switch (session?.state) {
    case 'draft': return 'waiting'
    case 'running': return 'running'
    case 'paused': return 'paused'
    case 'done': return 'done'
    case 'failed': return 'failed'
    case 'stopped': return 'stopped'
    default: return 'idle'
  }
}

const step = (n, [a, b]) => (n >= b ? 3 : n >= a ? 2 : 1)

// sizeStep is 1, 2 or 3: how much the session has spent or planned. Absolute
// thresholds, so a circle does not change size because a sibling did.
export function sizeStep(session) {
  const spent = Number(session?.spent_usd) || 0
  const tasks = Number(session?.counts?.total) || 0
  return Math.max(step(spent, [1, 5]), step(tasks, [5, 15]))
}

function circleOf(node) {
  const s = node.session
  const counts = s.counts || {}
  return {
    id: s.id,
    conversationId: s.conversation_id || '',
    title: String(s.title || '').trim() || `#${s.id}`,
    state: orbitState(s),
    // How many items wait for the person on this agent (an optional count on the session row, 0 when absent): the circle shows it apart from a plain draft.
    waiting: Math.max(0, Number(s.waiting) || 0),
    size: sizeStep(s),
    level: node.depth,
    childCount: node.children.length,
    tasksDone: Number(counts.done) || 0,
    tasksTotal: Number(counts.total) || 0,
    spent: Number(s.spent_usd) || 0,
    kind: s.kind || 'board',
    // A circle opens a chat only when the session has one.
    canOpen: !!s.conversation_id,
    // The operator recorded this session as the boss of the profile's open-ended run.
    boss: s.boss === true,
  }
}

// The client id of the boss session that stands behind a front-desk chat (tasks.BossClientID).
export const BOSS_KEY_PREFIX = 'boss-for-'
const OPEN_STATES = ['draft', 'running', 'paused']

// bossOf is the recorded boss of a chat: the session the operator stamped (the row's `boss`) whose client id names the
// chat. A session that only carries the key is not one.
export function bossOf(sessions, conversationId) {
  if (!conversationId) return null
  return (sessions || []).find(s => s && !s.archived && s.boss === true && s.client_id === BOSS_KEY_PREFIX + conversationId) || null
}

// activeBoss is the profile's open-ended run's boss session while it is open (one run at a time per profile): the
// newest recorded boss that is not archived, with its state, for the Agents page.
export function activeBoss(sessions) {
  const open = (sessions || []).filter(s => s && !s.archived && s.boss === true && OPEN_STATES.includes(s.state))
  return open.sort((a, b) => b.id - a.id)[0] || null
}

// frontDeskOf is the chat a boss session stands behind ('' when the key does not name one).
export function frontDeskOf(session) {
  const k = session?.client_id || ''
  return k.startsWith(BOSS_KEY_PREFIX) ? k.slice(BOSS_KEY_PREFIX.length) : ''
}

function findNode(roots, match) {
  const stack = [...roots]
  while (stack.length) {
    const n = stack.shift()
    if (match(n.session)) return n
    stack.unshift(...n.children)
  }
  return null
}

// orbitOf returns the orbit of one session: { root, satellites, overflow, total, level }.
// focus is { sessionId } or { conversationId } (a conversation that leads a session; the
// topmost one wins). root is null when the focus is not a session (a plain chat).
// Satellites are the focus's direct children, at most MAX_SATELLITES, urgent states first;
// the rest are overflow. A focus at MAX_LEVEL has none: the tree goes no deeper.
export function orbitOf(sessions, focus = {}, max = MAX_SATELLITES) {
  const empty = { root: null, satellites: [], overflow: [], total: 0, level: 0 }
  const roots = sessionTree((sessions || []).filter(s => s && !s.archived))
  let node = null
  if (focus.sessionId) node = findNode(roots, s => s.id === focus.sessionId)
  else if (focus.conversationId) node = findNode(roots, s => s.conversation_id === focus.conversationId)
  // A chat's boss is a sibling of its own session, never a child: it joins the ring first, whether or not the chat has a
  // mission session yet, and takes one of the slots.
  const bossRow = focus.conversationId ? bossOf(sessions, focus.conversationId) : null
  const bossNode = bossRow ? findNode(roots, s => s.id === bossRow.id) : null
  const bossCircle = bossNode ? circleOf(bossNode) : null
  if (!node && !bossCircle) return empty
  const circles = !node || node.depth >= MAX_LEVEL ? [] : node.children.map(c => circleOf(c))
  const sorted = circles
    .map((c, i) => [c, i])
    .sort((a, b) => URGENCY[a[0].state] - URGENCY[b[0].state] || a[1] - b[1])
    .map(x => x[0])
  const room = bossCircle ? Math.max(0, max - 1) : max
  return {
    root: node ? { ...circleOf(node) } : null,
    satellites: bossCircle ? [bossCircle, ...sorted.slice(0, room)] : sorted.slice(0, room),
    overflow: sorted.slice(room),
    total: sorted.length + (bossCircle ? 1 : 0),
    level: node ? node.depth : 0,
  }
}

const rad = d => (d * Math.PI) / 180
const round = n => Math.round(n * 100) / 100

// ringPositions places n satellites on a circle (arc 360) or an arc of `arc` degrees
// centred on `facing` (degrees, 0 = right, 90 = down), topDown to number an arc from its
// highest point to its lowest whichever side it faces. Returns [{x, y, angle}] from the
// orbit's centre; y grows downward. A full ring starts at the top.
export function ringPositions(n, { radius = 60, arc = 360, facing = 0, topDown = false } = {}) {
  const out = []
  for (let i = 0; i < n; i++) {
    let angle
    if (arc >= 360) angle = -90 + (360 * i) / n
    else if (n === 1) angle = facing
    else angle = facing - arc / 2 + (arc * i) / (n - 1)
    out.push({ x: round(radius * Math.cos(rad(angle))), y: round(radius * Math.sin(rad(angle))), angle: round(angle) })
  }
  return topDown ? out.sort((a, b) => a.y - b.y) : out
}

// linkPath is the thin curved link from the centre (0, 0) to a satellite at (x, y):
// a quadratic curve bent a little to one side, so links do not read as spokes.
export function linkPath(x, y, bend = 0.16) {
  const cx = x / 2 - y * bend
  const cy = y / 2 + x * bend
  return `M0 0 Q${round(cx)} ${round(cy)} ${round(x)} ${round(y)}`
}

// cycle returns the index after moving by delta in a ring of n (wraps both ways).
export const cycleIndex = (i, delta, n) => (n <= 0 ? -1 : (((i + delta) % n) + n) % n)
