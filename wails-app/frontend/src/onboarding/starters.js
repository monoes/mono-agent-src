// The first run's three starter workflows (internal/workflow/templates/starter_*.json)
// and the pure helpers the step uses to read a run. A card can run only when
// every step it needs is done; `fields` are the result keys the template's
// last node sets, shown in this order.
import { execStatus } from '../lib/execStatus.js'

export const STARTERS = [
  { templateId: 'starter_page_read', needs: [], fields: ['title', 'first_paragraph'] },
  { templateId: 'starter_summarise_page', needs: ['agents'], fields: ['summary'] },
  { templateId: 'starter_browser_read', needs: ['agents', 'browser', 'permissions'], fields: ['title', 'first_paragraph', 'description'] },
]

// The first step a starter needs that is not done (skipped counts as not done), or ''.
export function firstUnmet(starter, statuses) {
  return starter.needs.find(id => statuses[id] !== 'done') || ''
}

// A workflow's nodes in the order they run: the starters are straight chains
// laid out left to right.
export function orderNodes(nodes) {
  return (Array.isArray(nodes) ? nodes : [])
    .map(n => ({ id: n.id, name: n.name || n.node_type || n.id, x: Number(n.position_x) || 0 }))
    .sort((a, b) => a.x - b.x)
    .map(({ id, name }) => ({ id, name }))
}

const NODE_STATE = { SUCCESS: 'done', COMPLETED: 'done', RUNNING: 'running', WAITING: 'waiting', FAILED: 'failed', SKIPPED: 'skipped' }

// Each node's state from an execution detail (GetExecutionDetail), matched by
// node id, then by name. A node the detail does not list yet is 'todo'.
export function nodeStates(nodes, detail) {
  const rows = (detail && Array.isArray(detail.nodes)) ? detail.nodes : []
  return nodes.map(n => {
    const en = rows.find(r => r.node_id === n.id) || rows.find(r => r.node_name === n.name)
    return { ...n, state: (en && NODE_STATE[String(en.status || '').toUpperCase()]) || 'todo' }
  })
}

// The first item of the last node that produced one: [{ key, value }] for the
// starter's fields, else every plain value of that item.
export function resultFields(starter, nodes, detail) {
  const rows = (detail && Array.isArray(detail.nodes)) ? detail.nodes : []
  for (let i = nodes.length - 1; i >= 0; i--) {
    const en = rows.find(r => r.node_id === nodes[i].id) || rows.find(r => r.node_name === nodes[i].name)
    let items = null
    try { items = JSON.parse(en?.output_items || '[]') } catch { items = null }
    const json = Array.isArray(items) && items[0] && typeof items[0].json === 'object' ? items[0].json : null
    if (!json) continue
    const shown = v => (typeof v === 'string' ? v.trim() : (typeof v === 'number' || typeof v === 'boolean') ? String(v) : '')
    const wanted = (starter?.fields || []).map(key => ({ key, value: shown(json[key]) })).filter(f => f.value)
    if (wanted.length) return wanted
    return Object.keys(json).map(key => ({ key, value: shown(json[key]) })).filter(f => f.value)
  }
  return []
}

// Why a finished run did not succeed: the first failed node's error, the
// execution's, then the completion event's; '' when none says.
export function runError(detail, event) {
  const failed = ((detail && detail.nodes) || []).find(n => String(n.status || '').toUpperCase() === 'FAILED' && n.error_message)
  return String(failed?.error_message || detail?.error || event?.error || '').trim()
}

export const isSuccess = (detail) => execStatus(detail?.status).key === 'success'
export const isPartial = (detail) => execStatus(detail?.status).key === 'partial'
export const isLive = (detail) => execStatus(detail?.status).live

// browser.read at level "ask" raises an "allow this site?" Human-in-Loop item and
// returns (internal/browserctl/site.go SiteApprovalPendingError).
export const needsSiteApproval = (message) => /waits for the user's approval/i.test(String(message || ''))

// A run that failed because no AI agent is set up (wails-app app_workflows_run.go).
export const agentNotSetup = (event) => event?.code === 'agent_not_setup'
