import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Pause } from 'lucide-react'
import { confirm } from '../../ConfirmDialog.jsx'
import { pursuitSwitchApi as api } from '../../../lib/pursuitSwitchApi.js'
import './mission.css'

// What a Mission chat's pursuit is, what is missing for it, how to pause it, and the two switches that widen what a mission may do.
// The page renders the CLI's documents (`coder host pursuit status`, `task session pursuit readiness`) and shows the CLI's own words;
// it decides nothing. The notice stays until the person dismisses it (the dismissal is only a page preference). Every write here is
// the operator's click on a button; a refusal from the CLI is shown as it is.

const SEEN = 'monoagent-pursuit-notice-seen'
const seen = () => { try { return localStorage.getItem(SEEN) === '1' } catch { return false } }
const text = v => (typeof v === 'string' ? v : '')

export default function PursuitPanel({ sessionId, conversationId }) {
  const { t } = useTranslation()
  const [status, setStatus] = useState(null)
  const [ready, setReady] = useState(null)
  const [kids, setKids] = useState(null)
  const [approval, setApproval] = useState(null)
  const [ceiling, setCeiling] = useState('')
  const [hidden, setHidden] = useState(seen)
  const [busy, setBusy] = useState(false)
  const [note, setNote] = useState('')

  const load = useCallback(async () => {
    const [s, r, k, c] = await Promise.all([api.hostStatus(), api.readiness(sessionId), api.childrenState(), api.chatSettings(conversationId)]).catch(() => [null, null, null, null])
    setStatus(s); setReady(r); setKids(k)
    setApproval(c?.settings?.agent_approval ?? c?.agent_approval ?? null)
  }, [sessionId, conversationId])
  useEffect(() => { load() }, [load])

  const act = async (fn) => {
    setBusy(true); setNote('')
    try {
      const res = await fn()
      setNote(res?.error ? text(res.error) : '')
      await load()
    } catch (e) { setNote(String(e?.message || e)) } finally { setBusy(false) }
  }
  const dismiss = () => { try { localStorage.setItem(SEEN, '1') } catch { /* page preference only */ } setHidden(true) }
  const toggleChildren = async () => {
    const on = !kids?.missionChildrenEnabled
    if (!(await confirm(t(on ? 'pursuitPanel.confirmChildrenOn' : 'pursuitPanel.confirmChildrenOff')))) return
    act(() => api.setChildren(on))
  }
  const toggleApproval = async () => {
    const on = approval !== 'lead'
    const usd = Number(ceiling)
    if (on && !(usd > 0)) { setNote(t('pursuitPanel.needCeiling')); return }
    if (!(await confirm(t(on ? 'pursuitPanel.confirmApprovalOn' : 'pursuitPanel.confirmApprovalOff', { usd })))) return
    act(() => api.setApproval(conversationId, on, on ? usd : 0))
  }

  const missing = (ready?.items || []).filter(i => i?.status === 'missing')
  const notStarted = ready?.pursuit && ready.pursuit.started === false
  return (
    <div className="ms-pursuit" data-testid="pursuit-panel">
      {status?.notice && !hidden && (
        <div data-testid="pursuit-notice" role="note" aria-label={t('pursuitPanel.noticeLabel')}>
          <p>{text(status.notice)}</p>
          {status.missing && <p data-testid="pursuit-idle">{text(status.reason)}</p>}
          {status.pendingRestart && <p data-testid="pursuit-restart">{t('pursuitPanel.restartPending')}</p>}
          <button type="button" className="btn btn-ghost btn-sm" data-testid="pursuit-notice-dismiss" onClick={dismiss}>{t('pursuitPanel.gotIt')}</button>
        </div>
      )}
      <button type="button" className="btn btn-ghost btn-sm" data-testid="pursuit-pause" disabled={busy || !!notStarted} title={notStarted ? t('pursuitPanel.noPursuit') : undefined} onClick={() => act(() => api.pause(sessionId))}>
        <Pause size={11} aria-hidden="true" /> {t('pursuitPanel.pause')}
      </button>
      {ready?.error && <p data-testid="pursuit-ready-error">{t('pursuitPanel.readinessUnavailable')}</p>}
      {missing.length > 0 && (
        <div data-testid="pursuit-missing">
          <strong>{t('pursuitPanel.missingHeading')}</strong>
          <ul>{missing.map(i => <li key={i.id} data-testid={`pursuit-missing-${i.id}`}>{text(i.detail)}{i.fix ? <> <code>{text(i.fix)}</code></> : null}</li>)}</ul>
        </div>
      )}
      <div data-testid="pursuit-authority">
        <strong>{t('pursuitPanel.authorityHeading')}</strong>
        <label>
          <input type="checkbox" data-testid="pursuit-children" checked={!!kids?.missionChildrenEnabled} disabled={busy || !kids || !!kids.error} onChange={toggleChildren} /> {t('pursuitPanel.children')}
        </label>
        <label>
          <input type="checkbox" data-testid="pursuit-approval" checked={approval === 'lead'} disabled={busy || approval === null} onChange={toggleApproval} /> {t('pursuitPanel.approval')}
        </label>
        {approval !== 'lead' && (
          <input type="number" min="0" step="any" data-testid="pursuit-ceiling" aria-label={t('pursuitPanel.ceiling')} placeholder={t('pursuitPanel.ceiling')} value={ceiling} onChange={e => setCeiling(e.target.value)} />
        )}
      </div>
      {note && <p className="ms-error" role="alert" data-testid="pursuit-note">{note}</p>}
    </div>
  )
}
