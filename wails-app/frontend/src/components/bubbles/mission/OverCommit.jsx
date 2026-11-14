import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { AlertTriangle } from 'lucide-react'
import { sessionsApi } from '../../../services/taskSessions.js'

// What the CLI says about an over-committed mission (`over_committed` of `task session show`) and about its pursuit
// (`task session pursuit status`), shown as it is. Nothing is worked out here: whether it is over, by whom and by how
// much are the CLI's fields; the page only turns them into text, and never reads them as anything but text.
const cut = (v, n) => { const s = String(v ?? ''); return s.length > n ? s.slice(0, n - 1) + '…' : s }
// A dollar figure or an id the CLI sent: a number is shown as one, anything else is cut and shown as text.
const dollars = v => (typeof v === 'number' && Number.isFinite(v) ? `$${v.toFixed(2)}` : cut(v, 24))
const id = v => (Number.isInteger(v) && v > 0 ? `#${v}` : cut(v, 24))

export const isOver = oc => !!oc && typeof oc === 'object' && oc.over === true

// The chip: "Over-committed by $X: session #N". By another session than this one it names that one.
export function OverCommitChip({ overCommitted }) {
  const { t } = useTranslation()
  if (!isOver(overCommitted)) return null
  const text = t('bubbles.mission.overCommitted', { usd: dollars(overCommitted.over_by_usd), by: id(overCommitted.by_session_id) })
  return (
    <span className="ts-budget-flag" data-testid="mission-over-committed" data-level="over" role="img" aria-label={text} title={text}>
      <AlertTriangle size={11} aria-hidden="true" /> {text}
    </span>
  )
}

// The pursuit's waiting reason `over-committed` with its plain meaning. The status is read only while the mission is over
// (it is the CLI's `progress.waiting` that decides whether the pursuit waits for that reason).
export function PursuitOverCommitted({ sessionId, overCommitted, rev }) {
  const { t } = useTranslation()
  const [waiting, setWaiting] = useState('')
  const over = isOver(overCommitted)
  useEffect(() => {
    if (!over) { setWaiting(''); return undefined }
    let stopped = false
    sessionsApi.pursuitStatus(sessionId).then(doc => { if (!stopped) setWaiting(typeof doc?.progress?.waiting === 'string' ? doc.progress.waiting : '') })
    return () => { stopped = true }
  }, [sessionId, over, rev])
  if (!over || waiting !== 'over-committed') return null
  return <p className="ms-error" role="status" data-testid="pursuit-waiting-over-committed">{t('bubbles.mission.pursuitOverCommitted')}</p>
}
