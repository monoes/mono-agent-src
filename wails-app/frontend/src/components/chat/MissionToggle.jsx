import { useTranslation } from 'react-i18next'
import { Rocket } from 'lucide-react'

// The Mission switch of a coder chat and its "Mission" chip. State comes from useMissionToggle:
// on / locked (the chat is running a mission: shown on and disabled) / available (false below
// coding access) / error. The CLI enforces all of it; this only shows it. A locked state with a
// session id is a running mission; a locked state without one is a mission state the CLI could not
// read (the toggle is held as it is), and it is not shown as a running mission. The CLI's own reason
// (the session, how to stop it) is shown as it came.

const mono = 'var(--font-mono)'

export const missionLock = state => {
  const locked = !!state?.locked
  const running = locked && !!state?.sessionId
  return { locked, running, unreadable: locked && !running }
}

export function MissionChip({ state }) {
  const { t } = useTranslation()
  if (!state?.on || missionLock(state).unreadable) return null
  return (
    <span data-testid="mission-chip" title={state.locked ? t('missionToggle.locked') : t('missionToggle.chipOn')} style={{
      fontFamily: mono, fontSize: 9.5, color: 'var(--green)', border: '1px solid var(--green)', borderRadius: 999,
      padding: '1px 7px', whiteSpace: 'nowrap', display: 'inline-flex', alignItems: 'center', gap: 3,
    }}>
      <Rocket size={9} aria-hidden="true" /> {t('missionToggle.chip')}
    </span>
  )
}

export function MissionToggle({ toggle }) {
  const { t } = useTranslation()
  const { state, busy, error, set } = toggle
  if (!state && !error) return null
  const available = state ? state.available !== false : true
  const { locked, running, unreadable } = missionLock(state)
  const on = !!state?.on
  const lockedWhy = running ? (state.reason || t('missionToggle.runningSession', { id: state.sessionId }))
    : [t('missionToggle.unreadable'), state?.reason].filter(Boolean).join(': ')
  const why = locked ? lockedWhy : !available ? (state?.reason || t('missionToggle.needsAccess')) : t('missionToggle.hint')
  const disabled = busy || !state || locked || !available
  return (
    <>
      <button type="button" role="switch" aria-checked={on} aria-describedby="mission-toggle-why" disabled={disabled}
        data-testid="mission-toggle" title={why} onClick={() => set(!on)} className="btn btn-ghost btn-sm"
        style={{ fontFamily: mono, fontSize: 9, gap: 4, flexShrink: 0, opacity: locked ? 0.9 : undefined, color: on ? 'var(--green)' : 'var(--text-muted)', borderColor: on ? 'var(--green)' : undefined }}>
        <Rocket size={10} aria-hidden="true" /> {t('missionToggle.label')}: {on ? t('missionToggle.on') : t('missionToggle.off')}
      </button>
      <span id="mission-toggle-why" style={{ position: 'absolute', width: 1, height: 1, overflow: 'hidden', clip: 'rect(0 0 0 0)' }}>{why}</span>
      {(locked || !available) && <span data-testid="mission-toggle-reason" data-lock={running ? 'running' : unreadable ? 'unreadable' : undefined} style={{ fontFamily: mono, fontSize: 9, color: 'var(--text-muted)' }}>{why}</span>}
      {error && <span role="alert" data-testid="mission-toggle-error" style={{ flexBasis: '100%', fontFamily: mono, fontSize: 10, color: 'var(--red)' }}>{error}</span>}
    </>
  )
}
