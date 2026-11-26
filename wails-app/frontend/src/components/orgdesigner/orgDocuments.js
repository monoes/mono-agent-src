// Presentation helpers for the Documents panel. The view itself is built in Go
// (`monoagentcli org documents`, internal/orgbridge/docs_view.go); nothing here
// derives document state. The live stream only says "something changed".
import i18n from '../../i18n.js'

// monomind's version status -> what the panel calls it. `pending` is a
// published version nobody has decided yet.
export const STATUS_META = {
  pending: { get label() { return i18n.t('ui6OrgsCanvas.doc_published') }, color: 'var(--cyan, #22d3ee)' },
  accepted: { get label() { return i18n.t('ui6OrgsCanvas.doc_accepted') }, color: 'var(--green-neon, #4ade80)' },
  rejected: { get label() { return i18n.t('ui6OrgsCanvas.doc_rejected') }, color: 'var(--red, #ef4444)' },
  superseded: { get label() { return i18n.t('ui6OrgsCanvas.doc_superseded') }, color: 'var(--text-muted)' },
}

export function statusMeta(status) {
  return STATUS_META[status] || { label: String(status || i18n.t('ui6OrgsCanvas.doc_unknown')), color: 'var(--text-muted)' }
}

/** A version's label: a rejected version that was replaced says so. */
export function versionLabel(v) {
  const base = statusMeta(v?.status).label
  return v?.status === 'rejected' && v.superseded_by ? i18n.t('ui6OrgsCanvas.doc_superseded_by', { status: base, version: v.superseded_by }) : base
}

/**
 * True for a bus event that can change the document store: a document
 * notice from `org-docs` (published, rejected, rework exhausted), an
 * `org_doc_*` tool call, or a rework audit line. Anything else, including
 * unknown kinds and the old `loops` events, is false and never throws.
 */
export function isDocsEvent(ev) {
  if (!ev || typeof ev !== 'object') return false
  if (ev.type === 'message') return ev.from === 'org-docs'
  if (ev.type === 'tool') return typeof ev.tool === 'string' && ev.tool.startsWith('org_doc_')
  if (ev.type === 'audit') return typeof ev.reason === 'string' && ev.reason.includes('rework')
  return false
}

/** The CLI payload as a safe view, or null when it is an error or not a view. */
export function normalizeView(res) {
  if (!res || typeof res !== 'object' || res.error) return null
  const arr = (x) => (Array.isArray(x) ? x : [])
  return {
    ...res,
    runs: arr(res.runs),
    docs: arr(res.docs).filter(d => d && typeof d === 'object'),
    sections: arr(res.sections),
    summary: res.summary && typeof res.summary === 'object' ? res.summary : {},
  }
}
