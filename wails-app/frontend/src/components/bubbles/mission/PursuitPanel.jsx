import { Component, useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Pause } from 'lucide-react'
import { confirm } from '../../ConfirmDialog.jsx'
import { pursuitSwitchApi as api } from '../../../lib/pursuitSwitchApi.js'
import './mission.css'

// What a Mission chat's pursuit is, what is missing for it, how to pause it, and the two switches that widen what a mission may do.
// The page renders the CLI's documents (`coder host pursuit status`, `task session pursuit readiness`) and shows the CLI's own words;
// it decides nothing. Only the explanatory notice can be dismissed (per mission: a page preference); the status line (restart
// pending, idle reason, off, unreadable) is always shown. Every document is shape-checked before use, and a bad one can never take
// the mission strip down (the boundary below). Every write is the operator's click; a CLI refusal is shown as it is.

const seenKey = id => `monoagent-pursuit-notice-seen:${id}`
const seen = id => { try { return localStorage.getItem(seenKey(id)) === '1' } catch { return false } }
const str = v => (typeof v === 'string' ? v : '')
const obj = v => (v && typeof v === 'object' && !Array.isArray(v) ? v : {})
const items = v => (Array.isArray(v) ? v : []).filter(i => i && typeof i === 'object' && typeof i.id === 'string' && i.status === 'missing')

// statusLines: the always-visible lines, from the status document alone. Each is {id, text}.
export function statusLines(status, t) {
  const s = obj(status)
  if (s.error || !Object.keys(s).length) return []
  const out = []
  const pursuit = obj(obj(s.host).pursuit)
  if (s.readError) out.push({ id: 'read-error', text: t('pursuitPanel.readError') })
  else if (s.savedOperatorOff === true) out.push({ id: 'off-saved', text: t('pursuitPanel.offSaved') })
  if (s.operatorOff === true && pursuit.offSource === 'env') out.push({ id: 'off-env', text: t('pursuitPanel.offEnv') })
  if (s.operatorOff === true && pursuit.offSource === 'saved' && s.savedOperatorOff !== true && !s.readError) out.push({ id: 'off-host', text: t('pursuitPanel.offHost') })
  if (str(s.missing)) out.push({ id: 'idle', text: str(s.reason) || t('pursuitPanel.idle') })
  if (s.pendingRestart === true) out.push({ id: 'restart', text: t('pursuitPanel.restartPending') })
  return out
}

export class Boundary extends Component {
  state = { bad: false }
  static getDerivedStateFromError() { return { bad: true } }
  componentDidCatch() { /* a bad payload shows the fallback; nothing else is affected */ }
  render() { return this.state.bad ? <p className="ms-error" role="alert" data-testid="pursuit-panel-broken">{this.props.fallback}</p> : this.props.children }
}

function Panel({ sessionId, conversationId }) {
  const { t } = useTranslation()
  const [status, setStatus] = useState(null)
  const [ready, setReady] = useState(null)
  const [kids, setKids] = useState(null)
  const [approval, setApproval] = useState(null)
  const [ceiling, setCeiling] = useState('')
  const [hidden, setHidden] = useState(() => seen(sessionId))
  const [busy, setBusy] = useState(false)
  const [note, setNote] = useState('')
  const [done, setDone] = useState('')
  const req = useRef(0)

  useEffect(() => { setHidden(seen(sessionId)) }, [sessionId])
  // load: the answer of an older request (the session or chat changed since, or the panel is gone) is ignored
  const load = useCallback(async () => {
    const mine = ++req.current
    const r = await Promise.all([api.hostStatus(), api.readiness(sessionId), api.childrenState(), api.chatSettings(conversationId)]).catch(() => [null, null, null, null])
    if (mine !== req.current) return
    const [s, rd, k, c] = r
    setStatus(s); setReady(rd); setKids(k)
    const a = obj(obj(c).settings).agent_approval ?? obj(c).agent_approval
    setApproval(typeof a === 'string' ? a : null)
  }, [sessionId, conversationId])
  useEffect(() => { load(); return () => { req.current++ } }, [load])

  const act = async (fn, ok = '') => {
    setBusy(true); setNote(''); setDone('')
    try {
      const res = obj(await fn())
      setNote(res.error ? str(res.error) || t('pursuitPanel.failed') : '')
      setDone(res.error ? '' : ok)
      await load()
    } catch (e) { setNote(String(e?.message || e)) } finally { setBusy(false) }
  }
  const dismiss = () => { try { localStorage.setItem(seenKey(sessionId), '1') } catch { /* page preference only */ } setHidden(true) }
  const toggleChildren = async () => {
    const on = !obj(kids).missionChildrenEnabled
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

  const lines = statusLines(status, t)
  const notice = str(obj(status).notice)
  const missing = items(obj(ready).items)
  const pursuitDoc = obj(obj(ready).pursuit)
  const notStarted = pursuitDoc.started === false
  const k = obj(kids)
  return (
    <div className="ms-pursuit ms-panel" data-testid="pursuit-panel">
      {lines.length > 0 && (
        <ul className="ms-panel-status" data-testid="pursuit-status" aria-label={t('pursuitPanel.statusLabel')}>
          {lines.map(l => <li key={l.id} data-testid={`pursuit-status-${l.id}`}>{l.text}</li>)}
        </ul>
      )}
      {notice && !hidden && (
        <div className="ms-panel-notice" data-testid="pursuit-notice" role="note" aria-label={t('pursuitPanel.noticeLabel')}>
          <p>{notice}</p>
          <button type="button" className="btn btn-ghost btn-sm" data-testid="pursuit-notice-dismiss" onClick={dismiss}>{t('pursuitPanel.gotIt')}</button>
        </div>
      )}
      <div className="ms-panel-row">
        <button type="button" className="btn btn-ghost btn-sm" data-testid="pursuit-pause" disabled={busy || notStarted} aria-busy={busy} title={notStarted ? t('pursuitPanel.noPursuit') : undefined} onClick={() => act(() => api.pause(sessionId), t('pursuitPanel.paused'))}>
          <Pause size={11} aria-hidden="true" /> {t('pursuitPanel.pause')}
        </button>
        {done && <span role="status" data-testid="pursuit-done">{done}</span>}
      </div>
      {(obj(ready).error || (ready !== null && !Array.isArray(obj(ready).items))) && <p data-testid="pursuit-ready-error">{t('pursuitPanel.readinessUnavailable')}</p>}
      {missing.length > 0 && (
        <div data-testid="pursuit-missing">
          <strong>{t('pursuitPanel.missingHeading')}</strong>
          <ul>{missing.map(i => <li key={i.id} data-testid={`pursuit-missing-${i.id}`}>{str(i.detail)}{str(i.fix) ? <> <code>{str(i.fix)}</code></> : null}</li>)}</ul>
        </div>
      )}
      <fieldset className="ms-panel-authority" data-testid="pursuit-authority">
        <legend>{t('pursuitPanel.authorityHeading')}</legend>
        <label className="ms-panel-check">
          <input type="checkbox" data-testid="pursuit-children" checked={!!k.missionChildrenEnabled} disabled={busy || kids === null || !!k.error || typeof k.missionChildrenEnabled !== 'boolean'} onChange={toggleChildren} /> <span>{t('pursuitPanel.children')}</span>
        </label>
        <label className="ms-panel-check">
          <input type="checkbox" data-testid="pursuit-approval" checked={approval === 'lead'} disabled={busy || approval === null} onChange={toggleApproval} /> <span>{t('pursuitPanel.approval')}</span>
        </label>
        {approval !== 'lead' && (
          <input className="ms-panel-ceiling" type="number" min="0" step="any" data-testid="pursuit-ceiling" aria-label={t('pursuitPanel.ceiling')} placeholder={t('pursuitPanel.ceiling')} value={ceiling} onChange={e => setCeiling(e.target.value)} />
        )}
      </fieldset>
      {note && <p className="ms-error" role="alert" data-testid="pursuit-note">{note}</p>}
    </div>
  )
}

export default function PursuitPanel(props) {
  const { t } = useTranslation()
  return <Boundary key={props.sessionId} fallback={t('pursuitPanel.broken')}><Panel {...props} /></Boundary>
}
