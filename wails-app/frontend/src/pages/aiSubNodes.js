// AI sub-node connections, as n8n has them: a root node (an agent, a
// chain, …) takes sub-nodes (a chat model, memory, tools, …) on ports along
// its bottom edge; a sub-node attaches by the port on its top edge. Which
// ports a node has comes from its schema: `sub_inputs` ([{ handle, label,
// max?, required? }]) on a root, `sub_output` (the kind, e.g. "ai_tool") on
// a sub-node. A connection is stored like any other, with the kind as both
// handles: { source: sub-node, source_handle: kind, target: root,
// target_handle: kind }. These edges are not part of the main flow: the
// cycle check and auto-layout's ordering leave them out.

// AI_KINDS names each kind's palette group, in palette order.
export const AI_KINDS = {
  ai_languageModel: 'AI · Models',
  ai_memory:        'AI · Memory',
  ai_tool:          'AI · Tools',
  ai_outputParser:  'AI · Output parsers',
  ai_embedding:     'AI · Embeddings',
  ai_vectorStore:   'AI · Vector stores',
  ai_document:      'AI · Document loaders',
  ai_textSplitter:  'AI · Text splitters',
  ai_retriever:     'AI · Retrievers',
  ai_reranker:      'AI · Rerankers',
}

export const isAiHandle = h => typeof h === 'string' && h.startsWith('ai_')

// isAiEdge reports whether a canvas edge is a sub-node connection.
export const isAiEdge = e => isAiHandle(e?.sourcePortId) || isAiHandle(e?.targetPortId)

// mainEdges drops the sub-node connections.
export const mainEdges = edges => (edges || []).filter(e => !isAiEdge(e))

// aiPortsOf reads a schema's sub-node ports.
export function aiPortsOf(schema) {
  const subInputs = (Array.isArray(schema?.sub_inputs) ? schema.sub_inputs : [])
    .filter(s => isAiHandle(s?.handle))
    .map(s => ({ handle: s.handle, label: s.label || s.handle, max: s.max > 0 ? s.max : 0, required: !!s.required }))
  const subOutput = isAiHandle(schema?.sub_output) ? schema.sub_output : ''
  return { subInputs, subOutput }
}

const sameSubInputs = (a = [], b = []) =>
  a.length === b.length && a.every((s, i) => s.handle === b[i].handle && s.label === b[i].label && s.max === b[i].max && s.required === b[i].required)

// withAiPorts is node with the sub-node ports its schema declares. A
// sub-node has no main input or output (n8n's sub-nodes have none); it only
// attaches to a root. Returns node itself when nothing changes.
export function withAiPorts(node, schema) {
  const { subInputs, subOutput } = aiPortsOf(schema)
  const sub = !!subOutput
  if (sameSubInputs(node.subInputs, subInputs) && (node.subOutput || '') === subOutput &&
      (!sub || (!node.inputs?.length && !node.outputs?.length))) return node
  return { ...node, subInputs, subOutput, ...(sub ? { inputs: [], outputs: [] } : {}) }
}

// attachedCount is how many sub-nodes of a kind feed target.
export const attachedCount = (edges, targetId, handle) =>
  (edges || []).filter(e => e.target === targetId && isAiEdge(e) && e.targetPortId === handle).length

// checkSubConnection says whether source's sub_output may go into target's
// sub-input `handle`: { ok: true } or { ok: false, reason, message }.
export function checkSubConnection(source, target, handle, edges) {
  const fail = (reason, message) => ({ ok: false, reason, message })
  if (!source || !target) return fail('missing', 'Connection rejected: node not found.')
  if (source.id === target.id) return fail('self', 'Connection rejected: a node cannot feed itself.')
  const input = (target.subInputs || []).find(s => s.handle === handle)
  if (!input) return fail('kind', `Connection rejected: ${target.label || target.id} takes no ${handle} connection.`)
  if (source.subOutput !== handle) {
    const want = input.label
    return fail('kind', `Connection rejected: ${source.label || source.id} is not a ${want}; only a ${want} node connects to ${target.label || target.id}'s ${want} input.`)
  }
  if ((edges || []).some(e => e.source === source.id && e.target === target.id && e.targetPortId === handle)) {
    return fail('duplicate', 'Already connected.')
  }
  if (input.max && attachedCount(edges, target.id, handle) >= input.max) {
    return fail('max', `Connection rejected: ${target.label || target.id} takes at most ${input.max} ${input.label}${input.max === 1 ? '' : ' connections'}. Remove the one connected first.`)
  }
  return { ok: true }
}

// subEdge is the canvas edge from sub-node source into target's `handle`.
export const subEdge = (sourceId, targetId, handle) => ({
  source: sourceId, sourcePortId: handle, sourcePortIdx: 0,
  target: targetId, targetPortId: handle, targetPortIdx: 0,
})

// missingRequired lists the labels of node's required sub-inputs with
// nothing connected.
export function missingRequired(node, edges) {
  return (node?.subInputs || [])
    .filter(s => s.required && attachedCount(edges, node.id, s.handle) === 0)
    .map(s => s.label)
}

// freeSubInput is target's sub-input of `kind` when it can take one more
// sub-node, else null.
export function freeSubInput(target, kind, edges) {
  const input = (target?.subInputs || []).find(s => s.handle === kind)
  if (!input) return null
  return input.max && attachedCount(edges, target.id, kind) >= input.max ? null : input
}

// aiEdgePath is a sub-node connection: from the sub-node's top port up (or
// down) into the root's bottom port, a vertical-tangent cubic bezier.
export function aiEdgePath(sx, sy, tx, ty) {
  const dy = Math.max(40, Math.abs(sy - ty) * 0.5)
  return `M${sx},${sy} C${sx},${sy - dy} ${tx},${ty + dy} ${tx},${ty}`
}

// subChildren maps each node id to the ids of the sub-nodes attached to it,
// in the order of its sub-inputs, then of the edges. A sub-node connected
// to several roots is listed under the first.
export function subChildren(nodes, edges) {
  const byId = new Map((nodes || []).map(n => [n.id, n]))
  const placed = new Set()
  const out = new Map()
  const ai = (edges || []).filter(e => isAiEdge(e) && byId.has(e.source) && byId.has(e.target) && e.source !== e.target)
  for (const n of nodes || []) {
    const order = (n.subInputs || []).map(s => s.handle)
    const rank = h => { const i = order.indexOf(h); return i === -1 ? order.length : i }
    const kids = ai.filter(e => e.target === n.id)
      .map((e, i) => ({ e, i }))
      .sort((a, b) => rank(a.e.targetPortId) - rank(b.e.targetPortId) || a.i - b.i)
      .map(({ e }) => e.source)
    out.set(n.id, kids)
  }
  // Each sub-node under one parent only: the first that lists it.
  for (const [id, kids] of out) out.set(id, kids.filter(k => !placed.has(k) && placed.add(k)))
  return out
}

// autoLayout lays a workflow out left to right: main-flow nodes in columns
// by depth (sub-node connections ignored), each root's sub-nodes in a row
// below it, theirs below them. Returns { [id]: { x, y } }. nodeW and nodeH
// size the main-flow grid; boxH(node) is a node's drawn height; skip holds
// edges to leave out of the ordering (loop back-edges).
export function autoLayout(nodes, edges, { nodeW, nodeH, gapX, gapY, boxH = () => nodeH, origin = 60, skip = new Set() }) {
  const kids = subChildren(nodes, edges)
  const isSub = new Set([...kids.values()].flat())
  const main = (nodes || []).filter(n => !isSub.has(n.id))
  const flow = mainEdges(edges)
  const successors = {}, predecessors = {}
  main.forEach(n => { successors[n.id] = []; predecessors[n.id] = [] })
  for (const e of flow) {
    if (!successors[e.source] || !predecessors[e.target]) continue
    if (skip.has(e)) continue
    successors[e.source].push(e.target)
    predecessors[e.target].push(e.source)
  }
  // Column (depth) by BFS from the roots.
  const col = {}
  const queue = main.filter(n => predecessors[n.id].length === 0).map(n => ({ id: n.id, depth: 0 }))
  while (queue.length) {
    const { id, depth } = queue.shift()
    if (col[id] !== undefined && col[id] >= depth) continue
    col[id] = depth
    successors[id].forEach(sid => queue.push({ id: sid, depth: depth + 1 }))
  }
  main.forEach(n => { if (col[n.id] === undefined) col[n.id] = 0 })

  const byId = new Map((nodes || []).map(n => [n.id, n]))
  const seen = new Set()
  // width/below: the span of a node's sub-node tree, and how far it reaches
  // below the node's own box.
  const width = id => {
    if (seen.has(id)) return nodeW
    seen.add(id)
    const k = kids.get(id) || []
    const w = k.length ? k.reduce((s, c) => s + width(c), 0) + gapX * (k.length - 1) : 0
    seen.delete(id)
    return Math.max(nodeW, w)
  }
  const below = id => {
    if (seen.has(id)) return 0
    seen.add(id)
    const k = kids.get(id) || []
    const b = k.length ? gapY + Math.max(...k.map(c => boxH(byId.get(c)) + below(c))) : 0
    seen.delete(id)
    return b
  }

  const columns = []
  for (const n of main) (columns[col[n.id]] ||= []).push(n.id)
  const pos = {}
  let x = origin
  for (const ids of columns) {
    if (!ids) continue
    let y = origin
    for (const id of ids) {
      pos[id] = { x, y }
      const hasKids = (kids.get(id) || []).length > 0
      y += nodeH + gapY + (hasKids ? Math.max(0, boxH(byId.get(id)) - nodeH) + below(id) : 0)
    }
    x += Math.max(...ids.map(width)) + gapX
  }
  // Sub-nodes: a row under their parent, from its left edge.
  const place = (id, depth = 0) => {
    if (depth > 50) return
    let cx = pos[id].x
    const cy = pos[id].y + Math.max(nodeH, boxH(byId.get(id))) + gapY
    for (const c of kids.get(id) || []) {
      if (pos[c]) continue
      pos[c] = { x: cx, y: cy }
      place(c, depth + 1)
      cx += width(c) + gapX
    }
  }
  for (const n of main) place(n.id)
  return pos
}
