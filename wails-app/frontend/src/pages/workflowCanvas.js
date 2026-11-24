// Backend workflow -> canvas nodes/edges. One copy for every place a workflow
// is opened (open from the dashboard, open a run, Load, auto-restore).

// Node-type prefix -> palette category. A prefix not listed is its own category.
export const PREFIX_TO_CAT = {
  core: 'control', db: 'database', comm: 'communication', service: 'services', data: 'data',
  http: 'http', system: 'system', trigger: 'triggers', ai: 'ai',
  instagram: 'instagram', linkedin: 'linkedin', x: 'x', tiktok: 'tiktok',
}

export const categoryOf = (nodeType) => {
  const prefix = String(nodeType || '').split('.')[0]
  return PREFIX_TO_CAT[prefix] || prefix
}

// deps: { normalizeNodeType, catColor, getConfigFields, resolvePortIdx,
//         deriveInputs, deriveOutputs, canonicalOutputHandle }
export function workflowToCanvas(wf, deps) {
  const { normalizeNodeType, catColor, getConfigFields, resolvePortIdx, deriveInputs, deriveOutputs, canonicalOutputHandle } = deps
  const nodes = (wf.nodes || []).map(n => {
    const nt = normalizeNodeType(n.node_type || n.type || '')
    const cat = categoryOf(nt)
    return {
      id: n.id, label: n.name, subtype: nt, category: cat,
      x: n.position_x, y: n.position_y,
      config: n.config || {}, color: catColor(cat),
      schema: n.schema || null,
      configFields: n.schema?.fields ? n.schema.fields : getConfigFields(nt),
      inputs: deriveInputs(nt), outputs: deriveOutputs(nt, n.config),
      runStatus: null, runInputItems: null, runOutputs: null, runOutputItems: 0, runDuration: null, runError: null,
    }
  })
  const nodeById = {}
  nodes.forEach(n => { nodeById[n.id] = n })
  // Handles are port ids ('main', 'true', ...): resolve to indexes; legacy
  // numeric strings fall back to the parsed index.
  const edges = (wf.connections || []).map(c => {
    const src = nodeById[c.source_node_id]
    const sourcePortId = canonicalOutputHandle(src?.subtype, c.source_handle)
    return {
      id: c.id, source: c.source_node_id, sourcePortId,
      sourcePortIdx: resolvePortIdx(src?.outputs, sourcePortId),
      target: c.target_node_id, targetPortId: c.target_handle,
      targetPortIdx: resolvePortIdx(nodeById[c.target_node_id]?.inputs, c.target_handle),
    }
  })
  return { nodes, edges }
}
