import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { X, Pause, Play, Square, ListChecks, Check, Hourglass, Minus, Activity } from 'lucide-react'
import { sessionsApi } from '../../services/taskSessions.js'
import { notify } from '../../services/api.js'
import { confirm } from '../ConfirmDialog.jsx'
import { monogram } from '../../lib/coderBubbles.js'
import { OrbitRing, statusLine } from './OrbitRing.jsx'
import './orbit.css'

const STATE_ICON = { running: Activity, waiting: Hourglass, done: Check, failed: X, paused: Pause, idle: Minus }

// LitePaneHeader is the head of an opened agent's simple pane: its title and state,
// the circles of its own agents (one orbit deeper than the pane's parent had), the link
// to its task board, and pause and stop. There is nothing to configure here; that stays
// in the root pane. orbit is sessionOrbit.orbitOf's result for this agent's session.
export function LitePaneHeader({ title, orbit, streaming, onStopTurn, onOpenAgent, onBoard, onClose, onChanged }) {
  const { t } = useTranslation()
  const [busy, setBusy] = useState(false)
  const root = orbit?.root || null
  const state = root?.state || (streaming ? 'running' : 'idle')
  const Icon = STATE_ICON[state] || Minus
  const live = root && (state === 'running' || state === 'waiting' || state === 'paused')

  const act = async (call) => {
    setBusy(true)
    const res = await call()
    setBusy(false)
    if (res?.error) notify('chat', String(res.error))
    onChanged?.()
  }
  const stop = async () => {
    const ok = await confirm(t('bubbles.lite.stopBody'), { title: t('bubbles.lite.stopTitle'), confirmLabel: t('bubbles.lite.stop'), cancelLabel: t('bubbles.keepOpen'), danger: true })
    if (ok) await act(() => sessionsApi.stop(root.id))
  }

  const disc = (
    <div className="lite-disc" data-state={state} aria-hidden="true">{monogram(title)}</div>
  )
  return (
    <div className="lite-head" data-testid="lite-head">
      {orbit && orbit.satellites.length + orbit.overflow.length > 0
        ? <OrbitRing orbit={orbit} variant="ring" onOpen={onOpenAgent} label={t('bubbles.orbit.groupNamed', { name: title })}>{disc}</OrbitRing>
        : <div className="lite-disc-solo">{disc}</div>}
      <div className="lite-head-main">
        <h2 className="lite-title">{title}</h2>
        <span className="lite-state" data-state={state} role="status">
          <Icon size={11} strokeWidth={2.5} aria-hidden="true" />
          {root ? statusLine(t, root) : t(`bubbles.orbit.state.${state}`)}
        </span>
        <div className="lite-actions">
          {root && <button type="button" className="btn btn-ghost btn-sm" onClick={() => onBoard?.(root)} style={{ gap: 4, fontSize: 10 }}><ListChecks size={12} /> {t('bubbles.lite.board')}</button>}
          {live && state !== 'paused' && <button type="button" className="btn btn-ghost btn-sm" disabled={busy} onClick={() => act(() => sessionsApi.pause(root.id))} style={{ gap: 4, fontSize: 10 }}><Pause size={12} /> {t('bubbles.lite.pause')}</button>}
          {live && state === 'paused' && <button type="button" className="btn btn-ghost btn-sm" disabled={busy} onClick={() => act(() => sessionsApi.resume(root.id))} style={{ gap: 4, fontSize: 10 }}><Play size={12} /> {t('bubbles.lite.resume')}</button>}
          {live && <button type="button" className="btn btn-ghost btn-sm" disabled={busy} onClick={stop} style={{ gap: 4, fontSize: 10 }}><Square size={12} /> {t('bubbles.lite.stop')}</button>}
          {!live && streaming && <button type="button" className="btn btn-ghost btn-sm" onClick={onStopTurn} style={{ gap: 4, fontSize: 10 }}><Square size={12} /> {t('bubbles.lite.stopTurn')}</button>}
        </div>
      </div>
      <button type="button" className="btn btn-ghost btn-sm" onClick={onClose} title={t('bubbles.lite.close')} aria-label={t('bubbles.lite.close')} style={{ padding: '3px 6px', alignSelf: 'flex-start' }}>
        <X size={13} />
      </button>
    </div>
  )
}
