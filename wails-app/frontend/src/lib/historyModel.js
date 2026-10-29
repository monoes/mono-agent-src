// Pure rules for the unified history list (chats and org runs, one row each):
// how a row's status merges with an open bubble's live summary, how times and
// filters read, and how a delete result splits into outcomes.

import { orgBubbleKey } from './coderBubbles.js'

export const PAGE_SIZE = 30
export const ACCESS_LEVELS = ['scoped', 'coding', 'workspace', 'full']
export const KIND_FILTERS = ['chat', 'org']
export const STATUS_FILTERS = ['running', 'needs', 'error', 'done', 'idle']

// bubbleKeyOf is the key the bubble store uses for a row: a chat's key is its
// conversation id, an org run's is org:<name>#<runId> (org:<name> while the
// run id is not known).
export function bubbleKeyOf(row) {
  return row.kind === 'org' ? orgBubbleKey(row.orgName, row.runId || '') : row.id
}

// liveStatus overlays an open bubble's live summary on the stored row. Only
// states the stored row cannot know yet (a turn running now, a question
// waiting) override it; unread counts ride along.
export function liveStatus(row, summary) {
  const base = { status: row.status || 'idle', unread: 0, needs: 0 }
  if (!summary) return base
  const out = { ...base, unread: summary.unread || 0, needs: summary.needs || 0 }
  if (summary.status === 'working') out.status = 'running'
  else if (summary.status === 'needs') out.status = 'needs'
  return out
}

// relativeTime: "just now", "5m ago", "3h ago", "2d ago", else the date.
export function relativeTime(iso, now = Date.now()) {
  const t = Date.parse(iso)
  if (!iso || Number.isNaN(t)) return ''
  const s = Math.max(0, Math.round((now - t) / 1000))
  if (s < 45) return 'just now'
  if (s < 3600) return `${Math.max(1, Math.round(s / 60))}m ago`
  if (s < 86400) return `${Math.round(s / 3600)}h ago`
  if (s < 7 * 86400) return `${Math.round(s / 86400)}d ago`
  return new Date(t).toISOString().slice(0, 10)
}

// toFilter is the binding's filter object; linked asks the CLI for rows with
// any application link (--application any).
export function toFilter({ kind = '', status = '', query = '', linked = false } = {}) {
  return { kind, status, application: linked ? 'any' : '', query: query.trim() }
}


// sortKey is the backend's list order key (newest first).
export const sortKey = r => `${r.updatedAt}|${r.id}`

// reconcileRefresh merges a fresh first page into the rows already loaded.
// Rows the fresh page should have listed but did not (newer than its last
// row, or anywhere when the page is the whole list) are gone: finished or
// deleted. Rows beyond the page boundary are kept until the user scrolls.
export function reconcileRefresh(prev, first, hasMore) {
  if (!hasMore || first.length === 0) return first
  const boundary = sortKey(first[first.length - 1])
  const fresh = new Set(first.map(r => r.id))
  return [...first, ...prev.filter(r => !fresh.has(r.id) && sortKey(r) < boundary)]
}

// mergePage appends a page, dropping ids already shown (a refresh can shift rows).
export function mergePage(existing, page) {
  const seen = new Set(existing.map(r => r.id))
  return [...existing, ...page.filter(r => !seen.has(r.id))]
}

// deleteOutcome splits DeleteChatConversations' answer: ids gone, ids kept
// because a turn is running, ids the CLI declined to delete (kept), and ids
// that failed with the CLI's message.
export function deleteOutcome(res) {
  return {
    deleted: res?.deleted || [],
    active: res?.active || [],
    failed: Object.entries(res?.failed || {}).map(([id, message]) => ({ id, message })),
    // Kept ids were not deleted because the CLI declined: processes the chat left running could not be
    // stopped (survivors), or deleting it is the operator's (operator_only).
    kept: Object.entries(res?.kept || {}).map(([id, k]) => ({
      id, code: k?.code || '', message: k?.message || '', survivors: k?.survivors || [],
    })),
  }
}

// survivorNames lists the processes a delete could not stop, by command and pid, as many as fit in a notice.
export function survivorNames(survivors, max = 4) {
  const names = (survivors || []).map(s => `${s.command || 'process'} (${s.pid})`)
  return names.length > max ? `${names.slice(0, max).join(', ')}, +${names.length - max}` : names.join(', ')
}

// chatIdsOnly guards every delete path: org runs are never deleted from here.
export function chatIdsOnly(rows) {
  return rows.filter(r => r.kind === 'chat').map(r => r.id)
}

// orgRunOpening says how an org-run row opens as a bubble: { runId, finished }.
// A run that is running now (status is `org status` for the org: its current
// or last run in .run, null when it could not be read) opens the org's live
// bubble; any other run with an id opens read-only, as a finished run. A row
// with neither a run id nor a live run has nothing to open (null: the Orgs tab).
export function orgRunOpening(row, status) {
  const runId = row.runId || ''
  const current = status?.run || ''
  const liveNow = runId && current
    ? runId === current && status.status === 'running'
    : row.status === 'running'
  if (liveNow) return { runId: runId || current, finished: false }
  return runId ? { runId, finished: true } : null
}

// orgRunLabel is the row subtitle: "run <short id> · <date>".
export function orgRunLabel(row) {
  const id = (row.runId || '').slice(0, 8)
  const t = Date.parse(row.updatedAt)
  const date = Number.isNaN(t) ? '' : new Date(t).toISOString().slice(0, 10)
  return [id && `run ${id}`, date].filter(Boolean).join(' \u00b7 ')
}
