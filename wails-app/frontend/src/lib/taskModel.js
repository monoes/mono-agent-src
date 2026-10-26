// Pure logic of the Tasks tab (spec §10): the board document, the moves the
// page shows before the CLI answers, where a drop or a key puts a card,
// search, what changed between two reads, and what a card shows. No React
// and no DOM: the tests drive all of it.

import { splitNotes } from './taskNotes.js'

export const COLUMNS = ['inbox', 'ready', 'in_progress', 'review', 'done']

// A card new to these columns goes on top (newest first); Ready and In
// progress are queues (spec 4.6).
const NEWEST_FIRST = new Set(['inbox', 'review', 'done'])

export function defaultIndex(status, length) {
  return NEWEST_FIRST.has(status) ? 0 : length
}

const byPosition = (a, b) => (a.position - b.position) || (a.id - b.id)

// normalizeBoard turns the board document (as `task board --json` prints it)
// into the page's board: the five columns always present, each in position
// order.
export function normalizeBoard(doc) {
  const columns = {}
  for (const s of COLUMNS) columns[s] = [...(doc?.tasks?.[s] || [])].sort(byPosition)
  return {
    profile: doc?.profile || { id: '', name: '' },
    rev: doc?.rev ?? 0,
    counts: { ...(doc?.counts || {}) },
    columns,
  }
}

// findTask is where card id sits: {task, status, index}, or null.
export function findTask(board, id) {
  if (!board) return null
  for (const s of COLUMNS) {
    const index = board.columns[s].findIndex(t => t.id === id)
    if (index >= 0) return { task: board.columns[s][index], status: s, index }
  }
  return null
}

// insertAt is the index place gives in list (the column without the card);
// a ref that is not there falls back to the column's default.
function insertAt(list, status, place) {
  const at = list.findIndex(t => t.id === place?.ref)
  switch (place?.where) {
    case 'top': return 0
    case 'bottom': return list.length
    case 'before': return at < 0 ? defaultIndex(status, list.length) : at
    case 'after': return at < 0 ? defaultIndex(status, list.length) : at + 1
    default: return defaultIndex(status, list.length)
  }
}

// withCounts keeps the counts in step with the columns. Done is cut to its
// newest cards, so its count moves by what its column gained or lost.
function withCounts(board, columns) {
  const counts = { ...board.counts }
  for (const s of COLUMNS) {
    counts[s] = s === 'done'
      ? Math.max(0, (board.counts.done ?? board.columns.done.length) + columns.done.length - board.columns.done.length)
      : columns[s].length
  }
  return { ...board, columns, counts }
}

// applyMove is the board after moving card id to column `to` at place, as
// the store will do it: a card keeps its claim only by moving within In progress, and a
// move to the column the card is in with no place named changes nothing (spec
// 4.6: the default end belongs to a card new to a column).
export function applyMove(board, id, to, place) {
  const found = findTask(board, id)
  if (!found || !COLUMNS.includes(to)) return board
  if (found.status === to && !place?.where) return board
  const columns = { ...board.columns, [found.status]: board.columns[found.status].filter(t => t.id !== id) }
  const target = [...columns[to]]
  // Only a move within In progress keeps the claim (the store's rule, whatever a hand-edited row says);
  // the last event is the store's to name, so none is shown until the next read.
  const keepClaim = found.status === 'in_progress' && to === 'in_progress'
  const moved = { ...found.task, status: to, claim: keepClaim ? found.task.claim : null, last_event: null }
  target.splice(insertAt(target, to, place), 0, moved)
  columns[to] = target
  return withCounts(board, columns)
}

// applyRemove is the board without card id (archived).
export function applyRemove(board, id) {
  const found = findTask(board, id)
  if (!found) return board
  return withCounts(board, { ...board.columns, [found.status]: board.columns[found.status].filter(t => t.id !== id) })
}

// applyOps lays the pending operations ({type: 'move', id, to, place} or
// {type: 'remove', id}) over the last board read, oldest first.
export function applyOps(board, ops) {
  if (!board) return board
  return ops.reduce((b, op) => (op.type === 'remove' ? applyRemove(b, op.id) : applyMove(b, op.id, op.to, op.place)), board)
}

// placeFor turns a drop at index among ids (the target column's cards as
// shown, without the moved card) into the CLI's place, always next to a
// shown card: a search or the Done column cut to its newest cards must not
// bury the card among hidden ones. An empty column takes its default.
export function placeFor(ids, index) {
  if (ids.length === 0) return { where: '' }
  if (index >= ids.length) return { where: 'after', ref: ids[ids.length - 1] }
  return { where: 'before', ref: ids[Math.max(0, index)] }
}

// isNoopDrop says whether a drop leaves the card where it was: same column,
// same order. fromIds is the source column as shown, the card included.
export function isNoopDrop(fromIds, from, to, ids, index, id) {
  if (from !== to) return false
  const order = [...ids.slice(0, index), id, ...ids.slice(index)]
  return order.length === fromIds.length && order.every((x, i) => x === fromIds[i])
}

// dropIndex is where a card dropped at height y lands among rects (the
// column's shown cards without the dragged one, top to bottom): before the
// first card whose middle is below y.
export function dropIndex(rects, y) {
  const i = rects.findIndex(r => y < r.top + r.height / 2)
  return i < 0 ? rects.length : i
}

// keyMove is the move a key asks of card id on the board as shown:
// 'left'/'right' (Shift+arrows) go to the next column at its default place,
// 'up'/'down' (Alt+arrows) pass the shown neighbour. {to, place}, or
// {blocked: 'first' | 'last' | 'top' | 'bottom'}, or null for no such card.
export function keyMove(shown, id, key) {
  const found = findTask(shown, id)
  if (!found) return null
  const { status, index } = found
  const col = shown.columns[status]
  if (key === 'left' || key === 'right') {
    const to = COLUMNS[COLUMNS.indexOf(status) + (key === 'left' ? -1 : 1)]
    if (!to) return { blocked: key === 'left' ? 'first' : 'last' }
    return { to, place: { where: '' } }
  }
  if (key === 'up') {
    if (index === 0) return { blocked: 'top' }
    return { to: status, place: { where: 'before', ref: col[index - 1].id } }
  }
  if (key === 'down') {
    if (index === col.length - 1) return { blocked: 'bottom' }
    return { to: status, place: { where: 'after', ref: col[index + 1].id } }
  }
  return null
}

// readFirst says a card is read in the drawer before it leaves Inbox for
// Ready (plan ruling F6): its notes are not on the card, and Ready is where
// agents act on a task's whole text (spec D6). The card shows its whole
// title (no clamp), so the notes are what it can hide.
export function readFirst(task) {
  return !!splitNotes(task?.notes).text.trim()
}

// focusTarget is the card a plain arrow moves the focus to: above or below
// in the column, or the card at the same height (else the last) in the
// nearest column that has cards.
export function focusTarget(shown, id, key) {
  const found = findTask(shown, id)
  if (!found) return null
  const col = shown.columns[found.status]
  if (key === 'up') return col[found.index - 1]?.id ?? null
  if (key === 'down') return col[found.index + 1]?.id ?? null
  if (key !== 'left' && key !== 'right') return null
  const step = key === 'left' ? -1 : 1
  for (let c = COLUMNS.indexOf(found.status) + step; c >= 0 && c < COLUMNS.length; c += step) {
    const other = shown.columns[COLUMNS[c]]
    if (other.length) return other[Math.min(found.index, other.length - 1)].id
  }
  return null
}

// matches says whether task t answers query: every word must be in its
// title, notes, source (page, page title, app) or claimant; "#12" means
// task 12 exactly.
export function matches(t, query) {
  const words = String(query || '').toLowerCase().split(/\s+/).filter(Boolean)
  const hay = [t.title, splitNotes(t.notes).text, t.source?.url, t.source?.title, t.source?.app, t.claim?.by]
    .filter(Boolean).join('\n').toLowerCase()
  return words.every(w => (/^#\d+$/.test(w) ? t.id === Number(w.slice(1)) : hay.includes(w)))
}

// filterBoard is the board as a search shows it.
export function filterBoard(board, query) {
  if (!board || !String(query || '').trim()) return board
  const columns = {}
  for (const s of COLUMNS) columns[s] = board.columns[s].filter(t => matches(t, query))
  return { ...board, columns }
}

// places maps each card of a board to its column and claimant.
function places(board) {
  const m = new Map()
  for (const s of COLUMNS) for (const t of board.columns[s]) m.set(t.id, { s, by: t.claim?.by || '' })
  return m
}

// remoteChanges lists what someone other than the operator did between two
// reads of one profile's board: a card they moved to another column, a
// claim they took over, a card they added. Oldest first, the last max. The
// operator's own actions ("you") never count.
export function remoteChanges(prev, next, max = 3) {
  if (!prev || !next || prev.profile.id !== next.profile.id) return []
  const before = places(prev)
  const out = []
  for (const s of COLUMNS) {
    for (const t of next.columns[s]) {
      const ev = t.last_event
      if (!ev || ev.actor === 'you') continue
      const was = before.get(t.id)
      if (!was && ev.kind !== 'created') continue
      if (was && was.s === s && (s !== 'in_progress' || was.by === (t.claim?.by || ''))) continue
      out.push({ id: t.id, title: t.title, actor: ev.actor, kind: ev.kind, to: s, at: ev.at })
    }
  }
  out.sort((a, b) => String(a.at).localeCompare(String(b.at)))
  return out.slice(-max)
}

// transitions are the cards to animate after the board changed: entered
// (not there before), done (just moved into Done) and claimed (an agent just
// took it: it was not in progress under that agent before). Nothing on the
// first read or after a profile change.
export function transitions(prev, next) {
  const entered = new Set()
  const done = new Set()
  const claimed = new Set()
  if (!prev || !next || prev.profile.id !== next.profile.id) return { entered, done, claimed }
  const before = places(prev)
  for (const s of COLUMNS) {
    for (const t of next.columns[s]) {
      const was = before.get(t.id)
      if (!was) entered.add(t.id)
      else if (s === 'done' && was.s !== 'done') done.add(t.id)
      if (s === 'in_progress' && t.claim?.by && (!was || was.s !== s || was.by !== t.claim.by)) claimed.add(t.id)
    }
  }
  return { entered, done, claimed }
}

// hostOf is a URL's host without "www.", or ''.
export function hostOf(url) {
  try { return new URL(url).hostname.replace(/^www\./, '') } catch { return '' }
}

// sourceChip says how a card shows where it came from (spec §10): kind picks
// the icon (globe, app window, terminal, spark, board); text is the domain
// for Chrome, the app's name for the OS menu or who added an agent's task (an
// agent's name, chat, workflow:<id>, agent:<kind>), '' for the kind's own word.
export function sourceChip(t) {
  const s = t.source || {}
  switch (s.kind) {
    case 'chrome': return { kind: 'chrome', text: hostOf(s.url) || s.title || '' }
    case 'os': return { kind: 'os', text: s.app || '' }
    case 'agent': return { kind: 'agent', text: t.author || '' }
    case 'app': return { kind: 'app', text: '' }
    default: return { kind: 'cli', text: '' }
  }
}

// shortAge is a card's age as {unit, n}: now, minutes, hours, days, weeks.
export function shortAge(from, now) {
  const ms = now - Date.parse(from)
  if (!Number.isFinite(ms) || ms < 60000) return { unit: 'now', n: 0 }
  const m = Math.floor(ms / 60000)
  if (m < 60) return { unit: 'm', n: m }
  const h = Math.floor(m / 60)
  if (h < 24) return { unit: 'h', n: h }
  const d = Math.floor(h / 24)
  if (d < 7) return { unit: 'd', n: d }
  return { unit: 'w', n: Math.floor(d / 7) }
}

// claimState is a claim as a card shows it at now: who, the initial on the
// avatar, stale once the store said so or the lease ended (a lease ends
// with no write, so no read follows: the clock decides; a lease that ends
// exactly now has ended), and the minutes left or since the end.
export function claimState(claim, now) {
  if (!claim?.by) return null
  const until = Date.parse(claim.until)
  const stale = !!claim.stale || !Number.isFinite(until) || until <= now
  const minutes = Number.isFinite(until) ? Math.floor(Math.abs(until - now) / 60000) : 0
  const initial = (claim.by.match(/[A-Za-z0-9]/)?.[0] || '?').toUpperCase()
  return { by: claim.by, initial, stale, minutes, until: claim.until }
}

// splitMinutes is {h, m} for "1h 5m".
export function splitMinutes(minutes) {
  return { h: Math.floor(minutes / 60), m: minutes % 60 }
}

const ACTOR_COLORS = ['var(--purple-light)', 'var(--cyan)', 'var(--teal)', 'var(--orange)', 'var(--instagram)', 'var(--green-neon)']

// actorColor gives each agent name a steady colour, so two agents on one
// board read apart.
export function actorColor(name) {
  let h = 0
  for (const ch of String(name)) h = (h * 31 + ch.codePointAt(0)) >>> 0
  return ACTOR_COLORS[h % ACTOR_COLORS.length]
}

// isTypingTarget: a key pressed here belongs to a field (the search, a
// quick add, the drawer, the assistant's composer), not to the board.
export function isTypingTarget(el) {
  if (!el) return false
  return el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || el.tagName === 'SELECT' || el.isContentEditable === true
}
