import { hydrate, tidyTree } from './orgGraph.js'
import { layOutSections, parseSections, sectionsEnabled } from './sectionsGraph.js'
import { suggestIcon, loadIconManifest } from './roleIcons.js'

// Wraps hydrate() with a visual-only icon fallback: the AI chat is
// deliberately allowed to skip ui.icon (mastermind-createorg's own
// guidance — "never force-fit a role into a nearby-but-wrong archetype"),
// so most roles arrive icon-less. Rather than every icon-less role
// rendering the same hardcoded 'coder' avatar (RoleNode.jsx's fallback),
// resolve a sensible per-role match via suggestIcon()'s token-overlap
// scoring — memoized per role id, so this is deterministic across repeated
// hydrations and never needs to be written back to the org config.
export async function hydrateWithIcons(roles) {
  const nodes = hydrate(roles || [])
  const manifest = await loadIconManifest()
  const known = new Set(manifest.map(a => a.id))
  await Promise.all(nodes.map(async (n) => {
    if (n.icon && known.has(n.icon)) return
    n.icon = await suggestIcon({ id: n.id, title: n.title, type: n.type, ui: { icon: n.icon } })
  }))
  return nodes
}

// Collapse button shown inside an expanded palette/inspector panel.
export const panelFoldBtnStyle = {
  display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 4, flexShrink: 0,
  fontFamily: 'var(--font-mono)', fontSize: 9.5, padding: '5px 6px',
  background: 'transparent', border: 'none', borderBottom: '1px solid var(--border)',
  color: 'var(--text-muted)', cursor: 'pointer',
}

// Narrow vertical strip shown in place of a folded panel — click to expand.
// `borderSide` is which edge touches the canvas ('right' for the left
// panel's strip, 'left' for the right panel's strip).
export function panelStripStyle(borderSide) {
  const base = {
    width: 26, flexShrink: 0, minHeight: 0,
    display: 'flex', alignItems: 'flex-start', justifyContent: 'center', paddingTop: 10,
    background: 'transparent', border: 'none',
    color: 'var(--text-muted)', cursor: 'pointer',
  }
  return borderSide === 'right'
    ? { ...base, borderRight: '1px solid var(--border)' }
    : { ...base, borderLeft: '1px solid var(--border)' }
}

// layOutMissingPositions runs tidyTree scoped to just the nodes missing an
// x/y (freshly arrived from the CLI or an AI tool with no ui.x/ui.y) so a
// newly-added role gets a sane position without relocating anything the
// user has already placed by hand. Simple approach: if ANY node is missing
// a position, tidy the whole tree once (cheap for typical org sizes) but
// only overwrite x/y on the nodes that were actually missing one.
export function layOutMissingPositions(nodes, sectionsObj) {
  const missing = nodes.filter(n => typeof n.x !== 'number' || typeof n.y !== 'number')
  if (missing.length === 0) return nodes
  const sectionList = parseSections(sectionsObj)
  if (sectionsEnabled(sectionList)) return layOutSections(nodes, sectionList)
  const tidied = tidyTree(nodes)
  const tidiedById = new Map(tidied.map(n => [n.id, n]))
  return nodes.map(n => {
    if (typeof n.x === 'number' && typeof n.y === 'number') return n
    const t = tidiedById.get(n.id)
    return t ? { ...n, x: t.x, y: t.y } : { ...n, x: 0, y: 0 }
  })
}
