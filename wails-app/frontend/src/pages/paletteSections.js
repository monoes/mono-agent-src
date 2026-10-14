// The node palette is split in two: the actions of the installed web
// automations, and every other node. Which section a node belongs to comes
// from `node palette` (each node's `section`); the palette only groups.

import { AI_KINDS, isAiHandle } from './aiSubNodes.js'

export const WEB_AUTOMATION = 'web_automation'

// paletteSectionOf is a category's section, from its nodes.
export function paletteSectionOf(nodes) {
  return Array.isArray(nodes) && nodes.some(n => n?.section === WEB_AUTOMATION) ? WEB_AUTOMATION : 'nodes'
}

// paletteCategoryLabel is the automation's name for a web automation
// category, '' for a built-in one.
export function paletteCategoryLabel(nodes) {
  return (Array.isArray(nodes) && nodes.find(n => n?.category_label)?.category_label) || ''
}

// splitPaletteSections groups categories into the two sections, web
// automations first (sorted by name) and then the rest in their given order.
// Both sections are always returned, so an empty one can say so.
export function splitPaletteSections(categories) {
  const web = [], rest = []
  for (const c of categories || []) (c.section === WEB_AUTOMATION ? web : rest).push(c)
  web.sort((a, b) => String(a.label).localeCompare(String(b.label)))
  return [
    { id: WEB_AUTOMATION, label: 'Web automations', categories: web },
    { id: 'nodes', label: 'Nodes', categories: rest },
  ]
}

// AI_GROUP_PREFIX starts the id of an AI sub-node group (ai_sub:<kind>).
export const AI_GROUP_PREFIX = 'ai_sub:'

// groupAiSubNodes pulls the AI sub-nodes out of their categories into one
// group per kind, in AI_KINDS order, placed after the "ai" category (or at
// the end when there is none).
function groupAiSubNodes(cats) {
  const groups = new Map()
  const kept = []
  for (const c of cats) {
    const keep = []
    for (const n of c.nodes) {
      const kind = n.schema?.sub_output
      if (!isAiHandle(kind)) { keep.push(n); continue }
      if (!groups.has(kind)) groups.set(kind, [])
      groups.get(kind).push(n)
    }
    if (keep.length) kept.push({ ...c, nodes: keep })
  }
  if (!groups.size) return cats
  const order = Object.keys(AI_KINDS)
  const rank = k => { const i = order.indexOf(k); return i === -1 ? order.length : i }
  const ai = [...groups.keys()].sort((a, b) => rank(a) - rank(b) || a.localeCompare(b)).map(kind => ({
    id: AI_GROUP_PREFIX + kind, label: AI_KINDS[kind] || `AI · ${kind.slice(3)}`, section: 'nodes', nodes: groups.get(kind),
  }))
  const at = kept.findIndex(c => c.id === 'ai')
  return at === -1 ? [...kept, ...ai] : [...kept.slice(0, at + 1), ...ai, ...kept.slice(at + 1)]
}

export const DEPRECATED = 'deprecated'
const DEPRECATED_LABEL = /\(deprecated\)/i

// arrangePaletteCategories orders the built-in categories for a first edit:
// triggers first (every workflow starts with one), deprecated nodes pulled
// out of their categories into one collapsed "Deprecated" group at the end
// (still there for old workflows, out of the way of new ones), and a SERVICE
// node that shares its name with a node elsewhere labelled "(API)" so the two
// distinct node types can be told apart. AI sub-nodes (a schema
// `sub_output`) move into one group per kind after the AI category; root AI
// nodes stay in it. Nodes keep their own category.
export function arrangePaletteCategories(categories) {
  const deprecated = []
  const cats = []
  for (const c of categories || []) {
    const keep = []
    for (const n of c.nodes || []) {
      const node = { ...n, category: n.category || c.id }
      if (DEPRECATED_LABEL.test(node.label || '')) deprecated.push(node)
      else keep.push(node)
    }
    if (keep.length) cats.push({ ...c, nodes: keep })
  }
  const otherLabels = new Set()
  for (const c of cats) if (c.id !== 'service') for (const n of c.nodes) otherLabels.add(String(n.label).toLowerCase())
  const named = cats.map(c => (c.id !== 'service' ? c : {
    ...c,
    nodes: c.nodes.map(n => (otherLabels.has(String(n.label).toLowerCase()) ? { ...n, label: `${n.label} (API)` } : n)),
  }))
  const triggers = named.filter(c => c.id === 'triggers')
  const rest = groupAiSubNodes(named.filter(c => c.id !== 'triggers'))
  const out = [...triggers, ...rest]
  if (deprecated.length) out.push({ id: DEPRECATED, label: 'DEPRECATED', section: 'nodes', collapsedByDefault: true, nodes: deprecated })
  return out
}
