import { NODE_W, NODE_H } from './orgGraph'
import { RECENT_EDGE_MS } from './orgActivity.js'

/** Curved message edge between two card centres, bowed so A→B and B→A separate. */
export function messageEdgePath(a, b) {
  const ax = a.x + NODE_W / 2, ay = a.y + NODE_H / 2
  const bx = b.x + NODE_W / 2, by = b.y + NODE_H / 2
  const mx = (ax + bx) / 2, my = (ay + by) / 2
  const cx = mx - (by - ay) * 0.2
  const cy = my + (bx - ax) * 0.2
  return `M${ax},${ay} Q${cx},${cy} ${bx},${by}`
}

/** Message edges between roles on this canvas, marked recent within RECENT_EDGE_MS of now. */
export function liveEdgesFor(liveState, nodes, now) {
  if (!liveState) return []
  const byId = new Map(nodes.map(n => [n.id, n]))
  return Object.values(liveState.edges || {})
    .filter(e => byId.has(e.from) && byId.has(e.to) && e.from !== e.to)
    .map(e => ({ ...e, path: messageEdgePath(byId.get(e.from), byId.get(e.to)), recent: now - e.lastTs <= RECENT_EDGE_MS }))
}
