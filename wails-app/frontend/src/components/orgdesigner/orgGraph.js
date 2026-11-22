// Pure, dependency-free graph/geometry logic for the Org Designer canvas.
// No React/DOM here — everything is plain data in, plain data out, so this
// module is fully unit-testable in isolation (see orgGraph.test.js).
//
// Canvas node shape: { id, parentId, title, type, responsibilities, icon,
// color, x, y, rest }. `parentId` mirrors the wire field `reports_to`
// (single-parent tree, unlike NodeRunner's arbitrary multi-edge workflow
// graph) — see hydrate()/dehydrate() for the wire <-> canvas mapping.

export const NODE_W = 180
export const NODE_H = 72

// ── Tree structure ───────────────────────────────────────────────────────

/**
 * Build lookup structures for a flat array of canvas nodes.
 * Nodes whose parentId is null/undefined, or whose parentId doesn't resolve
 * to another node in the array, or whose parentId equals their own id, are
 * treated as roots (defensive: callers may pass transient/invalid states
 * while editing).
 */
export function buildTree(nodes) {
  const byId = new Map(nodes.map(n => [n.id, n]))
  const childrenOf = new Map()
  nodes.forEach(n => childrenOf.set(n.id, []))
  const roots = []
  nodes.forEach(n => {
    if (n.parentId != null && n.parentId !== n.id && byId.has(n.parentId)) {
      childrenOf.get(n.parentId).push(n)
    } else {
      roots.push(n)
    }
  })

  const depthOf = new Map()
  const assignDepth = (node, depth) => {
    depthOf.set(node.id, depth)
    ;(childrenOf.get(node.id) || []).forEach(c => assignDepth(c, depth + 1))
  }
  roots.forEach(r => assignDepth(r, 0))

  return { byId, childrenOf, depthOf, roots }
}

/**
 * Live-drag cycle check: would re-parenting `childId` under `newParentId`
 * create a cycle? Walks up newParentId's own parentId chain looking for
 * childId. The backend independently enforces the same rule server-side —
 * this only needs to be correct, not match its exact algorithm.
 */
export function wouldCycle(nodes, childId, newParentId) {
  if (newParentId === childId) return true
  const byId = new Map(nodes.map(n => [n.id, n]))
  let cur = byId.get(newParentId)
  const seen = new Set()
  while (cur) {
    if (cur.id === childId) return true
    if (seen.has(cur.id)) return false // already-cyclic elsewhere; not our concern here
    seen.add(cur.id)
    if (cur.parentId == null) return false
    cur = byId.get(cur.parentId)
  }
  return false
}

/** Every node in id's subtree (not including id), via BFS over children. */
export function descendantsOf(nodes, id) {
  const { childrenOf } = buildTree(nodes)
  const result = []
  const queue = [...(childrenOf.get(id) || [])]
  while (queue.length) {
    const n = queue.shift()
    result.push(n)
    queue.push(...(childrenOf.get(n.id) || []))
  }
  return result
}

/**
 * Validate the reports_to tree. Mirrors the exact wording a user would see
 * from `monomind org validate` or this app's own Go validator, plus real
 * cycle detection that monomind's own checkOrgStructure lacks.
 */
export function validateStructure(nodes) {
  const errors = []

  const idCounts = new Map()
  const byId = new Map()
  nodes.forEach(n => {
    idCounts.set(n.id, (idCounts.get(n.id) || 0) + 1)
    if (!byId.has(n.id)) byId.set(n.id, n)
  })
  const dupIds = [...idCounts.entries()].filter(([, c]) => c > 1).map(([id]) => id)
  if (dupIds.length) {
    errors.push(`duplicate role id(s): ${dupIds.join(', ')}`)
  }

  const selfReportIds = new Set()
  nodes.forEach(n => {
    if (n.parentId != null && n.parentId === n.id) {
      errors.push(`role "${n.id}" reports to itself`)
      selfReportIds.add(n.id)
    }
  })

  nodes.forEach(n => {
    if (n.parentId != null && n.parentId !== n.id && !byId.has(n.parentId)) {
      errors.push(`role "${n.id}": reports_to "${n.parentId}" matches no role id`)
    }
  })

  const roots = nodes.filter(n => n.parentId == null)
  if (roots.length === 0 && nodes.length > 0) {
    errors.push('no root role — exactly one role must have reports_to: null')
  } else if (roots.length > 1) {
    errors.push(`multiple root roles (${roots.map(r => r.id).join(', ')}) — exactly one may have reports_to: null`)
  }

  // Cycle detection — independent of root checks, so a cycle among non-root
  // roles is still caught even when a separate valid root exists (the case
  // monomind's own server-side validator misses).
  const reported = new Set(selfReportIds)
  nodes.forEach(n => {
    if (reported.has(n.id)) return
    const path = []
    const inPath = new Set()
    let cur = n
    let guard = 0
    while (cur && !reported.has(cur.id) && guard++ <= nodes.length) {
      if (inPath.has(cur.id)) {
        const startIdx = path.indexOf(cur.id)
        const cyclePath = path.slice(startIdx).concat(cur.id)
        errors.push(`circular reporting: ${cyclePath.join(' -> ')}`)
        cyclePath.forEach(id => reported.add(id))
        break
      }
      path.push(cur.id)
      inPath.add(cur.id)
      if (cur.parentId == null || !byId.has(cur.parentId)) break
      cur = byId.get(cur.parentId)
    }
  })

  return { valid: errors.length === 0, errors }
}

// ── Layout ────────────────────────────────────────────────────────────────

/**
 * Real tidy-tree layout (distinct from NodeRunner's naive BFS-by-column):
 * depth-based y, post-order x where a leaf claims the next free slot and an
 * internal node centers over its children's midpoint. Because leaf slots are
 * assigned by a single monotonically-increasing counter and internal x is
 * always within [min(children x), max(children x)], subtrees can never
 * overlap in x — no separate collision-resolution pass is needed for this
 * to be visually correct at typical org sizes (a few dozen roles).
 * Multiple roots (invalid-but-representable mid-edit) are laid out as
 * separate trees stacked with a vertical offset between them.
 * Returns a NEW array — does not mutate the input.
 */
export function tidyTree(nodes, opts = {}) {
  const { nodeW = NODE_W, nodeH = NODE_H, gapX = 36, gapY = 84 } = opts
  if (!nodes || !nodes.length) return []

  const byId = new Map(nodes.map(n => [n.id, n]))
  const childrenOf = new Map()
  nodes.forEach(n => childrenOf.set(n.id, []))
  const roots = []
  nodes.forEach(n => {
    if (n.parentId != null && n.parentId !== n.id && byId.has(n.parentId)) {
      childrenOf.get(n.parentId).push(n)
    } else {
      roots.push(n)
    }
  })

  const posX = new Map()
  const posY = new Map()
  let rowOffset = 0

  roots.forEach(root => {
    let nextLeafSlot = 0
    let maxDepth = 0
    const visit = (node, depth) => {
      maxDepth = Math.max(maxDepth, depth)
      posY.set(node.id, (depth + rowOffset) * (nodeH + gapY))
      const children = childrenOf.get(node.id) || []
      if (!children.length) {
        posX.set(node.id, nextLeafSlot * (nodeW + gapX))
        nextLeafSlot++
        return
      }
      children.forEach(c => visit(c, depth + 1))
      const xs = children.map(c => posX.get(c.id))
      posX.set(node.id, (Math.min(...xs) + Math.max(...xs)) / 2)
    }
    visit(root, 0)
    rowOffset += maxDepth + 1
  })

  return nodes.map(n => ({
    ...n,
    x: posX.has(n.id) ? posX.get(n.id) : n.x,
    y: posY.has(n.id) ? posY.get(n.id) : n.y,
  }))
}

// ── Connector paths ──────────────────────────────────────────────────────

/**
 * Vertical-biased cubic bezier connector, parent-above/child-below
 * convention (org-chart, not NodeRunner's horizontal workflow ports).
 * sx,sy = the CHILD's top handle (edge start — visually the lower point,
 * since the child renders below the parent).
 * tx,ty = the PARENT's bottom handle (edge end — visually the upper point).
 * The curve runs from the child's top edge up to the parent's bottom edge.
 */
export function reportPath(sx, sy, tx, ty) {
  const dy = Math.max(36, Math.abs(ty - sy) * 0.45)
  return `M${sx},${sy} C${sx},${sy - dy} ${tx},${ty + dy} ${tx},${ty}`
}

/**
 * Orthogonal alternative: vertical-horizontal-vertical with small rounded
 * corners (8px radius, via quadratic bezier arcs at the two bends) — a raw
 * sharp-cornered V/H/V path looks broken, so this rounds it.
 */
export function elbowPath(sx, sy, tx, ty) {
  const midY = (sy + ty) / 2
  const r = Math.max(0, Math.min(8, Math.abs(tx - sx) / 2, Math.abs(midY - sy), Math.abs(ty - midY)))
  const dirX = tx >= sx ? 1 : -1
  const dirY1 = midY >= sy ? 1 : -1
  const dirY2 = ty >= midY ? 1 : -1
  return [
    `M${sx},${sy}`,
    `V${midY - dirY1 * r}`,
    `Q${sx},${midY} ${sx + dirX * r},${midY}`,
    `H${tx - dirX * r}`,
    `Q${tx},${midY} ${tx},${midY + dirY2 * r}`,
    `V${ty}`,
  ].join(' ')
}

/** Given a node's top-left {x,y}, the top-center handle (parent's dropzone). */
export function topHandlePos(node) {
  return { x: node.x + NODE_W / 2, y: node.y }
}

/** Given a node's top-left {x,y}, the bottom-center handle (child's dropzone). */
export function bottomHandlePos(node) {
  return { x: node.x + NODE_W / 2, y: node.y + NODE_H }
}

/**
 * Camera transform {x, y, zoom} that fits all node bounding boxes into the
 * viewport with ~10% padding, clamped to zoom [0.15, 2.5]. Convention:
 * screenX = worldX * zoom + x, screenY = worldY * zoom + y.
 */
export function fitCamera(nodes, viewportWidth, viewportHeight) {
  if (!nodes || !nodes.length) return { x: 0, y: 0, zoom: 1 }

  let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity
  nodes.forEach(n => {
    minX = Math.min(minX, n.x)
    minY = Math.min(minY, n.y)
    maxX = Math.max(maxX, n.x + NODE_W)
    maxY = Math.max(maxY, n.y + NODE_H)
  })

  const boxW = Math.max(1, maxX - minX)
  const boxH = Math.max(1, maxY - minY)
  const pad = 0.1
  const zoomX = viewportWidth / (boxW * (1 + pad * 2))
  const zoomY = viewportHeight / (boxH * (1 + pad * 2))
  let zoom = Math.min(zoomX, zoomY)
  if (!isFinite(zoom) || zoom <= 0) zoom = 1
  zoom = Math.min(2.5, Math.max(0.15, zoom))

  const cx = (minX + maxX) / 2
  const cy = (minY + maxY) / 2
  return {
    x: viewportWidth / 2 - cx * zoom,
    y: viewportHeight / 2 - cy * zoom,
    zoom,
  }
}

// ── Wire <-> canvas mapping ─────────────────────────────────────────────

const WIRE_MODELED_FIELDS = new Set(['id', 'title', 'type', 'reports_to', 'responsibilities', 'ui'])

/**
 * Convert wire-shape roles (array of {id, title, type, reports_to,
 * responsibilities, ui, ...unknownFields}) into canvas node shape.
 * Roles with no ui.x/ui.y (freshly created by the CLI or an AI tool) get
 * x/y left undefined — the caller (OrgDesigner.jsx) runs tidyTree on just
 * the newly-arrived subtree, not this pure function's job.
 */
export function hydrate(orgRoles) {
  return (orgRoles || []).map(role => {
    const rest = {}
    Object.keys(role).forEach(k => {
      if (!WIRE_MODELED_FIELDS.has(k)) rest[k] = role[k]
    })
    const ui = role.ui || {}
    return {
      id: role.id,
      title: role.title,
      type: role.type,
      parentId: role.reports_to ?? null,
      responsibilities: role.responsibilities,
      icon: ui.icon,
      color: ui.color,
      x: ui.x,
      y: ui.y,
      rest,
    }
  })
}

/**
 * Inverse of hydrate(): canvas nodes back to wire-shape roles. node.rest's
 * unmodeled fields (policy, adapter_config, provider, instructions_file,
 * runtime, max_turns_per_message, budget_tokens, budget_usd, etc.) are
 * spread first so they survive a hydrate -> dehydrate round trip unchanged;
 * the explicitly-modeled fields are applied after so they always win their
 * own keys.
 */
export function dehydrate(canvasNodes) {
  return (canvasNodes || []).map(node => {
    const { id, title, type, parentId, responsibilities, icon, color, x, y, rest } = node
    return {
      ...(rest || {}),
      id,
      title,
      type,
      reports_to: parentId ?? null,
      responsibilities,
      ui: { x, y, icon, color },
    }
  })
}
