// What the editor shows when a run fails or ends with errors — UI concerns
// only. The facts (status, the execution's error, each node's error) come
// from `workflow execution <id> --json` via GetExecutionDetail.

import i18n from '../i18n.js'

const PROBLEM_STATUSES = new Set(['FAILED', 'SUCCESS_WITH_ERRORS'])

// A run refused because the workflow is switched off (app_workflows.go
// runWorkflowProcess; the engine's ErrWorkflowInactive says the same).
export function isInactiveRunError(message) {
  return /\bis (inactive|not active)\b/i.test(String(message || ''))
}

// runProblem summarises a finished execution for the error panel, or returns
// null when there is nothing to explain. nodes are the canvas nodes, used to
// name the failing ones and, when the engine failed a run before any node
// executed (e.g. "unknown node type: gemini.generate_image"), to point at the
// node the error is about.
export function runProblem(detail, nodes = []) {
  const status = String(detail?.status || '').toUpperCase()
  if (!PROBLEM_STATUSES.has(status)) return null
  const byId = new Map(nodes.map(n => [n.id, n]))
  const failed = []
  for (const en of detail.nodes || []) {
    if (String(en.status || '').toUpperCase() !== 'FAILED') continue
    const node = byId.get(en.node_id) || nodes.find(n => n.label === en.node_name)
    failed.push({ id: node?.id || en.node_id, name: en.node_name || node?.label || en.node_id, error: en.error_message || '' })
  }
  const error = String(detail.error || '').trim()
  if (failed.length === 0 && error) {
    for (const node of nodesNamedIn(error, nodes)) failed.push({ id: node.id, name: node.label, error, inferred: true })
  }
  if (!error && failed.length === 0) {
    return { status, error: status === 'FAILED' ? i18n.t('ui6Node.runNoMessage') : '', failed }
  }
  return { status, error, failed }
}

// nodesNamedIn: the canvas nodes an execution-level error is about — by node
// type ("unknown node type: x.y") or by quoted node name.
function nodesNamedIn(error, nodes) {
  const typeMatch = /unknown node type:?\s*"?([\w.-]+)/i.exec(error)
  if (typeMatch) {
    const hits = nodes.filter(n => n.subtype === typeMatch[1])
    if (hits.length) return hits
  }
  return nodes.filter(n => n.label && (error.includes(`"${n.label}"`) || error.includes(`'${n.label}'`)))
}
