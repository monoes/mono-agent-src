import { useTranslation } from 'react-i18next'
import { Rocket } from 'lucide-react'

// The Mission switch of a coder chat and its "Mission" chip. State comes from useMissionToggle:
// on / locked (the chat is running a mission: shown on and disabled) / available (false below
// coding access) / error. The CLI enforces all of it; this only shows it.

const mono = 'var(--font-mono)'

export function MissionChip({ state }) {
  const { t } = useTranslation()
  if (!state?.on) return null
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
  const locked = !!state?.locked
  const on = !!state?.on
  const why = locked ? t('missionToggle.locked') : !available ? t('missionToggle.needsAccess') : t('missionToggle.hint')
  const disabled = busy || !state || locked || !available
  return (
    <>
      <button type="button" role="switch" aria-checked={on} aria-describedby="mission-toggle-why" disabled={disabled}
        data-testid="mission-toggle" title={why} onClick={() => set(!on)} className="btn btn-ghost btn-sm"
        style={{ fontFamily: mono, fontSize: 9, gap: 4, flexShrink: 0, color: on ? 'var(--green)' : 'var(--text-muted)', borderColor: on ? 'var(--green)' : undefined }}>
        <Rocket size={10} aria-hidden="true" /> {t('missionToggle.label')}: {on ? t('missionToggle.on') : t('missionToggle.off')}
      </button>
      <span id="mission-toggle-why" style={{ position: 'absolute', width: 1, height: 1, overflow: 'hidden', clip: 'rect(0 0 0 0)' }}>{why}</span>
      {(locked || !available) && <span data-testid="mission-toggle-reason" style={{ fontFamily: mono, fontSize: 9, color: 'var(--text-muted)' }}>{why}</span>}
      {error && <span role="alert" data-testid="mission-toggle-error" style={{ flexBasis: '100%', fontFamily: mono, fontSize: 10, color: 'var(--red)' }}>{error}</span>}
    </>
  )
}
