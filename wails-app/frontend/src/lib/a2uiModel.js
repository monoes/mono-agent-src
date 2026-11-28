// Pure fold of ui.surface / ui.action events into surface state (A2UI v1.0,
// https://a2ui.org/specification/v1.0-a2ui/). No React, no side effects.
// A surface: { surfaceId, provenance, agentId, ref, components: {id: comp},
// dataModel, status: 'live'|'resolved', lastSeq, partIndex }.

const BLOCKED = new Set(['__proto__', 'constructor', 'prototype'])
// Bounds on the fold, whatever the sender's own caps were.
export const MAX_SEGMENTS = 32 // JSON Pointer depth
export const MAX_SURFACE_COMPONENTS = 500 // entries read per message, and kept per surface

export function newSurface(surfaceId, meta = {}) {
  return {
    surfaceId,
    provenance: meta.provenance === 'system' ? 'system' : 'agent',
    agentId: meta.agentId || '',
    ref: meta.ref || null,
    components: {},
    dataModel: {},
    status: 'live',
    lastSeq: 0,
    partIndex: meta.partIndex ?? 0,
  }
}

export function parsePointer(path) {
  if (path === '' || path === '/' || path == null) return []
  if (path[0] !== '/') return null
  const segs = path.slice(1).split('/', MAX_SEGMENTS + 1)
  if (segs.length > MAX_SEGMENTS) return null
  return segs.map(s => s.replace(/~1/g, '/').replace(/~0/g, '~'))
}

function isObj(v) { return v !== null && typeof v === 'object' }

// Immutable JSON-Pointer set. value === undefined deletes. Returns the new root.
export function setPointer(root, path, value) {
  const segs = parsePointer(path)
  if (segs === null || segs.some(s => BLOCKED.has(s))) return root
  if (segs.length === 0) return value === undefined || value === null ? {} : value
  const put = (node, i) => {
    const key = segs[i]
    const last = i === segs.length - 1
    const isArr = Array.isArray(node)
    const base = isArr ? node.slice() : { ...(isObj(node) ? node : {}) }
    const k = isArr && /^\d+$/.test(key) ? Number(key) : key
    if (last) {
      if (value === undefined) { if (isArr) base.splice(k, 1); else delete base[k] } else base[k] = value
    } else {
      base[k] = put(base[k], i + 1)
    }
    return base
  }
  return put(root, 0)
}

export function getPointer(root, path) {
  const segs = parsePointer(path)
  if (segs === null) return undefined
  let cur = root
  for (const s of segs) {
    if (!isObj(cur) || BLOCKED.has(s) || !Object.prototype.hasOwnProperty.call(cur, s)) return undefined
    cur = cur[s]
  }
  return cur
}

function mergeComponents(map, list) {
  const next = { ...map }
  const items = Array.isArray(list) ? list : []
  let size = Object.keys(next).length
  for (let i = 0; i < items.length && i < MAX_SURFACE_COMPONENTS; i++) {
    const c = items[i]
    if (!c || typeof c.id !== 'string' || BLOCKED.has(c.id)) continue
    if (!Object.hasOwn(next, c.id)) {
      if (size >= MAX_SURFACE_COMPONENTS) continue
      size += 1
    }
    next[c.id] = c
  }
  return next
}

// applyMessage folds one A2UI envelope into a surface (or null = none yet).
// Returns the new surface, or null when the message deletes it.
export function applyMessage(surface, msg, meta = {}) {
  if (!isObj(msg)) return surface
  if (msg.deleteSurface) return null
  let s = surface
  if (msg.createSurface) {
    const c = msg.createSurface
    s = newSurface(c.surfaceId, meta)
    s.components = mergeComponents({}, c.components)
    if (c.dataModel !== undefined) s.dataModel = setPointer({}, '/', c.dataModel)
    return s
  }
  if (!s) return s
  if (msg.updateComponents) return { ...s, components: mergeComponents(s.components, msg.updateComponents.components), status: s.status }
  if (msg.updateDataModel) {
    const u = msg.updateDataModel
    const value = u.value === null ? undefined : u.value
    return { ...s, dataModel: setPointer(s.dataModel, u.path ?? '/', value) }
  }
  return s
}

// The host's card ids (internal/a2ui SystemSurfacePrefixes): no agent may name one.
const SYSTEM_ID = /^(hil|send|ask|mission)-/

// The host's card of a worker's question.
export const isAskCard = sf => sf?.provenance === 'system' && String(sf.surfaceId).startsWith('ask-')

// May this event touch surface cur (null: none yet)? The host may replace
// anything. An agent never names a host card, and touches only its own
// surfaces: same provenance, same author. An event that does not say it is the
// host's is an agent's.
function allowed(cur, payload) {
  if (payload.provenance === 'system') return true
  if (SYSTEM_ID.test(payload.surfaceId)) return false
  return !cur || (cur.provenance === 'agent' && cur.agentId === (payload.agentId || ''))
}

// applySurfaceEvent folds one ui.surface payload into a surfaces list,
// ignoring seqs already applied to that surface (replay safe).
export function applySurfaceEvent(surfaces, payload, seq, partIndex = 0) {
  const id = payload?.surfaceId
  if (typeof id !== 'string' || !id) return surfaces
  let msg = payload.message
  if (typeof msg === 'string') { try { msg = JSON.parse(msg) } catch { return surfaces } }
  const idx = surfaces.findIndex(s => s.surfaceId === id)
  const cur = idx === -1 ? null : surfaces[idx]
  if (!allowed(cur, payload)) return surfaces
  if (cur && typeof seq === 'number' && seq <= cur.lastSeq) return surfaces
  // A createSurface starts the surface anew: it belongs to whoever sent it.
  const meta = cur && !msg?.createSurface
    ? { provenance: cur.provenance, agentId: cur.agentId, ref: cur.ref, partIndex: cur.partIndex }
    : { provenance: payload.provenance, agentId: payload.agentId, ref: payload.ref, partIndex: cur ? cur.partIndex : partIndex }
  const next = applyMessage(cur, msg, meta)
  if (next === null) return idx === -1 ? surfaces : surfaces.filter((_, i) => i !== idx)
  if (next === cur) return surfaces
  const stamped = { ...next, lastSeq: typeof seq === 'number' ? seq : (cur?.lastSeq || 0) }
  if (idx === -1) return [...surfaces, stamped]
  const out = surfaces.slice(); out[idx] = stamped
  return out
}

// applyActionEvent marks an agent's surface by the outcome the host reported.
// A system surface is left alone: the CLI itself replaces it (a notice, or the
// next card such as a running mission whose Stop button must stay usable).
export function applyActionEvent(surfaces, payload) {
  const idx = surfaces.findIndex(s => s.surfaceId === payload?.surfaceId)
  if (idx === -1 || surfaces[idx].provenance === 'system') return surfaces
  const status = payload.outcome === 'accepted' ? 'resolved' : payload.outcome === 'stale' ? 'resolved' : 'live'
  const out = surfaces.slice()
  out[idx] = { ...surfaces[idx], status, lastOutcome: payload.outcome, lastReason: payload.reason || '' }
  return out
}

// The CLI journals a press (`chat ui action`) and the cards it or `chat ui
// sync-hil` writes as prompt-less finished turns of their own.
export const isJournalTurn = turnId => /^system-(uiaction|uisurface)-/.test(String(turnId || ''))

// foldConversationSurfaces folds the surface log of every turn (oldest first,
// [{key, state}]) into one picture, so a later turn can update, replace or
// remove a card an earlier one made. Returns {turnKey: [surface]}: each card
// sits where it was first made; a CLI journal turn that re-creates a card (the
// "decided" notice) puts it back in that same place.
export function foldConversationSurfaces(turns) {
  const log = []
  turns.forEach((turn, t) => ((turn.state && turn.state.surfaceLog) || []).forEach((e, i) => {
    log.push({ ...e, key: turn.key, order: [Date.parse(e.at) || 0, t, i] })
  }))
  log.sort((a, b) => a.order[0] - b.order[0] || a.order[1] - b.order[1] || a.order[2] - b.order[2])
  let list = []
  const places = new Map()
  const counts = new Map() // events applied per surface: its lastSeq, unmoved by other cards
  log.forEach(e => {
    if (e.type === 'action') { list = applyActionEvent(list, e.payload); return }
    const id = e.payload?.surfaceId
    const had = list.some(sf => sf.surfaceId === id)
    counts.set(id, (counts.get(id) || 0) + 1)
    list = applySurfaceEvent(list, e.payload, counts.get(id), e.partIndex)
    if (!had && list.some(sf => sf.surfaceId === id) && !(isJournalTurn(e.key) && places.has(id))) {
      places.set(id, { key: e.key, partIndex: e.partIndex })
    }
  })
  const byTurn = {}
  for (const sf of list) {
    const at = places.get(sf.surfaceId)
    ;(byTurn[at.key] ||= []).push({ ...sf, partIndex: at.partIndex })
  }
  return byTurn
}
