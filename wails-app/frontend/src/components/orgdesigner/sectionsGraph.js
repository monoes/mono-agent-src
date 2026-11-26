// Pure helpers for the Org Designer's sections view (monomind org-runtime
// §6.7). No React/DOM. Section rules themselves (one section per role, a
// lead per section, no direct cross-section message link) are enforced by
// the Go mutators in internal/orgdesign; this module only reads the
// `sections` object off the org document and does the geometry.
//
// Layout: a section's container is derived from where its members sit (each
// role's position is stored in its own opaque `ui` field), so there is no
// separate section layout to store: monomind rejects unknown keys inside a
// section and warns on unknown top-level ones.

import { NODE_W, NODE_H } from './orgGraph.js'
import i18n from '../../i18n.js'

export const SECTION_PAD = 28
export const SECTION_HEADER = 32
const LANE_COLS = 3
const GAP_X = 36
const GAP_Y = 56
const LANE_GAP = 140

const SECTION_COLORS = ['#38bdf8', '#f59e0b', '#34d399', '#f472b6', '#a78bfa', '#fb7185']
export const sectionColor = (i) => SECTION_COLORS[i % SECTION_COLORS.length]

const asList = (v) => (Array.isArray(v) ? v.filter(x => typeof x === 'string') : [])

/** The org document's `sections` object as an ordered array of plain sections. */
export function parseSections(sections) {
  if (!sections || typeof sections !== 'object' || Array.isArray(sections)) return []
  return Object.entries(sections)
    .filter(([, s]) => s && typeof s === 'object')
    .map(([name, s], i) => {
      const members = asList(s.members)
      const lead = typeof s.lead === 'string' ? s.lead : ''
      const leadId = lead || members[0] || ''
      return {
        name, members, lead, leadId,
        roster: lead && !members.includes(lead) ? [...members, lead] : members,
        publishes: asList(s.publishes), consumes: asList(s.consumes), writes: asList(s.writes),
        budgetUsd: typeof s.budget?.usd === 'number' ? s.budget.usd : null,
        maxReworkRounds: Number.isInteger(s.max_rework_rounds) ? s.max_rework_rounds : null,
        color: sectionColor(i),
      }
    })
}

/** Mirrors the Go SectionsEnabled: at least one section with something in it. */
export const sectionsEnabled = (list) => (list || []).some(s => s.roster.length > 0)

export const sectionOfRole = (list, id) => (list || []).find(s => s.roster.includes(id))?.name || ''

const isEndpoint = (n) => n?.rest?.kind === 'endpoint'

/** Container rectangles {name,x,y,w,h,color} around each section's placed roster. */
export function sectionRects(nodes, list) {
  const byId = new Map((nodes || []).map(n => [n.id, n]))
  const out = []
  for (const s of list || []) {
    const placed = s.roster.map(id => byId.get(id)).filter(n => n && typeof n.x === 'number' && typeof n.y === 'number')
    if (!placed.length) continue
    const x0 = Math.min(...placed.map(n => n.x)) - SECTION_PAD
    const y0 = Math.min(...placed.map(n => n.y)) - SECTION_PAD - SECTION_HEADER
    const x1 = Math.max(...placed.map(n => n.x + NODE_W)) + SECTION_PAD
    const y1 = Math.max(...placed.map(n => n.y + NODE_H)) + SECTION_PAD
    out.push({ name: s.name, x: x0, y: y0, w: x1 - x0, h: y1 - y0, color: s.color })
  }
  return out
}

/**
 * What dropping `node` where it now sits means. `rects` are the containers as
 * they were when the drag began. { kind: 'stay' } | { kind: 'move', to } |
 * { kind: 'refuse', reason }.
 */
export function dropMembership(rects, list, node) {
  if (isEndpoint(node)) return { kind: 'stay' }
  const cx = node.x + NODE_W / 2
  const cy = node.y + NODE_H / 2
  const hits = rects.filter(r => cx >= r.x && cx <= r.x + r.w && cy >= r.y && cy <= r.y + r.h)
  if (node.parentId == null) {
    return hits.length
      ? { kind: 'refuse', reason: i18n.t('ui6OrgsDesigner.refuse_root', { id: node.id }) }
      : { kind: 'stay' }
  }
  const cur = sectionOfRole(list, node.id)
  if (hits.some(r => r.name === cur)) return { kind: 'stay' }
  if (hits.length) return { kind: 'move', to: hits[0].name }
  return { kind: 'refuse', reason: i18n.t('ui6OrgsDesigner.refuse_outside', { id: node.id }) }
}

/** The container under world point (x, y), or null. */
export function sectionAtPoint(rects, x, y) {
  return (rects || []).find(r => x >= r.x && x <= r.x + r.w && y >= r.y && y <= r.y + r.h) || null
}

/** Document handoffs: each publisher of a type to each consumer of it. */
export function documentEdges(list) {
  const out = []
  for (const p of list || []) {
    for (const type of p.publishes) {
      for (const c of list) if (c.consumes.includes(type)) out.push({ type, from: p.name, to: c.name })
    }
  }
  return out
}

/** Inline validation for a sections org, worded like monomind's. */
export function sectionErrors(nodes, list, lng) {
  if (!sectionsEnabled(list)) return []
  const errors = []
  for (const n of nodes || []) {
    if (n.parentId != null && !isEndpoint(n) && !sectionOfRole(list, n.id)) {
      errors.push(i18n.t('ui6OrgsDesigner.err_outside_section', { id: n.id, ...(lng && { lng }) }))
    }
  }
  return errors
}

/** `validateStructure`'s result with the section errors added. */
export function withSectionErrors(structural, nodes, list) {
  const extra = sectionErrors(nodes, list)
  if (!extra.length) return structural
  const english = [...(structural.english || []), ...sectionErrors(nodes, list, 'en')]
  return { ...structural, valid: false, errors: [...structural.errors, ...extra], english }
}

/**
 * Positions for nodes that have none, in a sections org: the root on top,
 * one lane per section side by side, the lead leading its lane. Nodes that
 * already have x/y are left exactly where they are.
 */
export function layOutSections(nodes, list) {
  if (!sectionsEnabled(list)) return nodes
  const missing = new Set(nodes.filter(n => typeof n.x !== 'number' || typeof n.y !== 'number').map(n => n.id))
  if (!missing.size) return nodes
  const pos = new Map()
  const step = NODE_W + GAP_X
  const rowH = NODE_H + GAP_Y
  const top = SECTION_PAD + SECTION_HEADER + NODE_H + GAP_Y + 40
  // Lanes start right of anything already placed so new sections never overlap old ones.
  let laneX = nodes.reduce((m, n) => (typeof n.x === 'number' && !missing.has(n.id) ? Math.max(m, n.x + NODE_W + LANE_GAP) : m), 0)
  const firstLaneX = laneX
  for (const s of list) {
    const todo = s.roster.filter(id => missing.has(id))
    if (!todo.length) continue
    const others = s.roster.filter(id => id !== s.leadId)
    const cols = Math.min(LANE_COLS, Math.max(1, others.length))
    if (missing.has(s.leadId)) pos.set(s.leadId, { x: laneX + ((cols - 1) * step) / 2, y: top })
    others.forEach((id, k) => {
      if (missing.has(id)) pos.set(id, { x: laneX + (k % cols) * step, y: top + rowH * (1 + Math.floor(k / cols)) })
    })
    laneX += Math.max(1, cols) * step + LANE_GAP
  }
  const lanesW = Math.max(step, laneX - LANE_GAP - firstLaneX)
  for (const n of nodes) {
    if (!missing.has(n.id) || pos.has(n.id)) continue
    // The root (and anything in no section) sits above the lanes, centred.
    pos.set(n.id, { x: firstLaneX + (lanesW - NODE_W) / 2, y: 0 })
  }
  return nodes.map(n => (pos.has(n.id) ? { ...n, ...pos.get(n.id) } : n))
}

/** Curved connector from container a to container b, offset to keep parallel edges apart. */
export function sectionEdgePath(a, b, offset = 0) {
  const acx = a.x + a.w / 2, acy = a.y + a.h / 2
  const bcx = b.x + b.w / 2, bcy = b.y + b.h / 2
  const dx = bcx - acx, dy = bcy - acy
  if (Math.abs(dx) >= Math.abs(dy)) {
    const sx = dx >= 0 ? a.x + a.w : a.x, tx = dx >= 0 ? b.x : b.x + b.w
    const sy = acy + offset, ty = bcy + offset
    const k = Math.max(40, Math.abs(tx - sx) * 0.4) * (dx >= 0 ? 1 : -1)
    return { d: `M${sx},${sy} C${sx + k},${sy} ${tx - k},${ty} ${tx},${ty}`, mid: { x: (sx + tx) / 2, y: (sy + ty) / 2 } }
  }
  const sy = dy >= 0 ? a.y + a.h : a.y, ty = dy >= 0 ? b.y : b.y + b.h
  const sx = acx + offset, tx = bcx + offset
  const k = Math.max(40, Math.abs(ty - sy) * 0.4) * (dy >= 0 ? 1 : -1)
  return { d: `M${sx},${sy} C${sx},${sy + k} ${tx},${ty - k} ${tx},${ty}`, mid: { x: (sx + tx) / 2, y: (sy + ty) / 2 } }
}

/** A section name free in `list`, based on `base`. */
export function freeSectionName(list, base = 'section') {
  const taken = new Set((list || []).map(s => s.name))
  if (!taken.has(base)) return base
  let i = 2
  while (taken.has(`${base}-${i}`)) i++
  return `${base}-${i}`
}

/** Section a new role should join, and the role it reports to. */
export function placeNewRole({ list, nodes, anchorId, pointRect, selectedSection }) {
  const byId = new Map((nodes || []).map(n => [n.id, n]))
  const anchor = anchorId ? byId.get(anchorId) : null
  let section = (anchor && sectionOfRole(list, anchor.id)) || pointRect?.name || selectedSection || ''
  if (!section && list.length === 1) section = list[0].name
  if (!section) return null
  const s = list.find(x => x.name === section)
  const parent = anchor && sectionOfRole(list, anchor.id) === section ? anchor.id : s.leadId
  return { section, parent }
}

const overlaps = (a, b, m) =>
  a.x < b.x + NODE_W + m && b.x < a.x + NODE_W + m && a.y < b.y + NODE_H + m && b.y < a.y + NODE_H + m

/**
 * A spot for a role joining the container `rect` that overlaps no other
 * node: `prefer` if it is free and inside, else the first free grid slot
 * inside the container, else a new row just below it (the container then
 * grows to hold it). `excludeId` is the role being moved.
 */
export function freeSlotIn(nodes, rect, excludeId, prefer) {
  const others = (nodes || []).filter(n => n.id !== excludeId && typeof n.x === 'number' && typeof n.y === 'number')
  const free = (p) => !others.some(n => overlaps(p, n, GAP_X / 2))
  const inside = (p) => p.x >= rect.x + SECTION_PAD && p.x + NODE_W <= rect.x + rect.w - SECTION_PAD
    && p.y >= rect.y + SECTION_HEADER + SECTION_PAD && p.y + NODE_H <= rect.y + rect.h - SECTION_PAD
  if (prefer && inside(prefer) && free(prefer)) return { x: prefer.x, y: prefer.y }
  const stepX = NODE_W + GAP_X, stepY = NODE_H + GAP_Y
  const x0 = rect.x + SECTION_PAD, y0 = rect.y + SECTION_HEADER + SECTION_PAD
  const cols = Math.max(1, Math.floor((rect.w - 2 * SECTION_PAD + GAP_X) / stepX))
  const rows = Math.max(1, Math.floor((rect.h - SECTION_HEADER - 2 * SECTION_PAD + GAP_Y) / stepY))
  for (let r = 0; r < rows; r++) {
    for (let c = 0; c < cols; c++) {
      const p = { x: x0 + c * stepX, y: y0 + r * stepY }
      if (free(p)) return p
    }
  }
  let y = y0 + rows * stepY
  for (;;) {
    for (let c = 0; c < cols; c++) {
      const p = { x: x0 + c * stepX, y }
      if (free(p)) return p
    }
    y += stepY
  }
}
