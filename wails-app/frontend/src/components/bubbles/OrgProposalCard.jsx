import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ShieldCheck } from 'lucide-react'
import { api } from '../../services/api.js'
import './mission/mission.css'

// The card of an org proposal a chat tool made (set_org_grant, set_org_autonomy; #1477). The tool result is only a
// POINTER: the card reads the stored proposal through the CLI and shows ITS preview and hash, never the result's text.
// Confirm sends the hash the card displayed, so the CLI refuses a row rewritten after it was drawn. The conversation
// on the row is a label, not ownership, so it is checked here only to keep another chat's card out of this one; the
// authority is the CLI's operator-only confirm.

const VERB_OF_TOOL = { set_org_grant: 'grant.add', set_org_autonomy: 'autonomy.set' }
const ID = /^[0-9a-f]{32}$/
const PREFIX = 'mcp__monoagent__'

// proposalRefs lists the proposals the calls of one turn made: tool name and id, from a completed, non-failed call
// whose result is the JSON object the tools print. Anything else yields nothing.
export function proposalRefs(calls) {
  const out = []
  const seen = new Set()
  Object.values(calls || {}).forEach(call => {
    const name = typeof call?.name === 'string' && call.name.startsWith(PREFIX) ? call.name.slice(PREFIX.length) : call?.name
    if (!VERB_OF_TOOL[name] || call.status !== 'completed' || call.ok === false || typeof call.result !== 'string') return
    let r
    try { r = JSON.parse(call.result) } catch { return }
    const id = r && typeof r === 'object' ? r.proposal_id : null
    if (typeof id !== 'string' || !ID.test(id) || seen.has(id)) return
    seen.add(id)
    out.push({ key: call.callId || id, tool: name, id })
  })
  return out
}

// The CLI's refusals as plain sentences; any other error is shown as the CLI said it.
function plainError(t, msg) {
  const m = String(msg || '')
  if (/stale/.test(m)) return t('bubbles.orgProposal.err.stale')
  if (/expired/.test(m)) return t('bubbles.orgProposal.err.expired')
  if (/hash/.test(m)) return t('bubbles.orgProposal.err.hash')
  if (/already|decided/.test(m)) return t('bubbles.orgProposal.err.decided')
  return m
}

export default function OrgProposalCard({ tool, id, conversationId }) {
  const { t } = useTranslation()
  const [row, setRow] = useState(null)       // the stored proposal, as last read
  const [missing, setMissing] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [applied, setApplied] = useState(null) // the CLI's result of a confirm
  const alive = useRef(true)
  useEffect(() => { alive.current = true; return () => { alive.current = false } }, [])

  const load = useCallback(async () => {
    const res = await api.getOrgProposal(id)
    if (!alive.current) return
    if (res?.error || !res?.proposal || res.proposal.id !== id) { setMissing(true); return }
    setMissing(false)
    setRow(res.proposal)
  }, [id])
  useEffect(() => { load() }, [load])

  const foreign = !!row && (!conversationId || row.conversation_id !== conversationId || row.verb !== VERB_OF_TOOL[tool])
  if (missing || foreign) {
    return (
      <section className="ms-card" data-testid="org-proposal" aria-label={t('bubbles.orgProposal.unavailableLabel')}>
        <p className="ms-muted" data-testid="org-proposal-unavailable" role="alert">{t('bubbles.orgProposal.unavailable')}</p>
      </section>
    )
  }
  if (!row) return null

  const expired = row.status === 'pending' && Date.parse(row.expires_at) <= Date.now()
  const status = expired ? 'expired' : row.status
  // Only a pending, unexpired proposal whose org is as it was is offered; the CLI judges the same again at the click.
  const actionable = status === 'pending' && row.current !== false
  const act = async (verb) => {
    setBusy(true)
    setError('')
    try {
      // The hash is the one this card displays (row.hash), not one read again at click time.
      const res = verb === 'confirm' ? await api.confirmOrgProposal(id, row.hash) : await api.declineOrgProposal(id)
      if (res?.error) setError(plainError(t, res.error))
      else if (verb === 'confirm') setApplied(res?.result ?? null)
      await load()
    } finally { if (alive.current) setBusy(false) }
  }
  return (
    <section className="ms-card" data-testid="org-proposal" data-status={status} aria-label={t('bubbles.orgProposal.label', { org: row.org })}>
      <div className="ms-card-head"><ShieldCheck size={12} aria-hidden="true" /> <strong>{t('bubbles.orgProposal.title', { org: row.org })}</strong>
        <span className={`ts-chip ts-chip--${status}`} data-testid="org-proposal-status">{t(`bubbles.orgProposal.status.${status}`, { defaultValue: status })}</span>
      </div>
      <pre className="ms-text" data-testid="org-proposal-preview" style={{ whiteSpace: 'pre-wrap', fontFamily: 'var(--font-mono)', margin: '6px 0 0' }}>{row.preview_text}</pre>
      <p className="ms-muted" data-testid="org-proposal-meta">
        {t('bubbles.orgProposal.by', { by: row.created_by })} · {t('bubbles.orgProposal.expires', { at: row.expires_at })} · <span data-testid="org-proposal-hash" title={row.hash}>{String(row.hash).slice(0, 12)}</span>
      </p>
      {status === 'pending' && row.current === false && <p className="ms-error" role="alert" data-testid="org-proposal-changed">{t('bubbles.orgProposal.err.stale')}</p>}
      {actionable && <p className="ms-muted">{t('bubbles.orgProposal.exactly')}</p>}
      {error && <p className="ms-error" role="alert" data-testid="org-proposal-error">{error}</p>}
      {applied != null && status === 'applied' && <pre className="ms-muted" data-testid="org-proposal-result" style={{ whiteSpace: 'pre-wrap', margin: '6px 0 0' }}>{JSON.stringify(applied, null, 2)}</pre>}
      {(actionable || (status === 'pending' && row.current === false)) && (
        <div className="ms-actions">
          {actionable && <button type="button" className="btn btn-primary btn-sm" data-testid="org-proposal-confirm" disabled={busy} onClick={() => act('confirm')}>{t('bubbles.orgProposal.confirm')}</button>}
          <button type="button" className="btn btn-ghost btn-sm" data-testid="org-proposal-decline" disabled={busy} onClick={() => act('decline')}>{t('bubbles.orgProposal.decline')}</button>
        </div>
      )}
    </section>
  )
}
