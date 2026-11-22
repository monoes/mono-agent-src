// "Finish setting up": the onboarding steps still skipped or not done, at the
// top of the dashboard. Each row reopens the onboarding at its step
// (OnboardingContext). The rows come from the gate's state once loadFull()
// has read it with every step's facts (the gate's first, fast read leaves out
// a completed install's facts), read when the card shows and again whenever
// the dashboard is shown again. Welcome and You're set are never rows: they
// are not setup left to do. Hiding it is remembered (getStarted.js, the key
// the old Get started checklist used).
import { useCallback, useContext, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Circle, Rocket, X } from 'lucide-react'
import { OnboardingContext } from '../../onboarding/onboardingContext.js'
import { STEPS, statusesOf, railLabel } from '../../onboarding/steps.js'
import { loadDismissed, saveDismissed } from './getStarted.js'

const SETUP_STEPS = STEPS.filter(s => s.id !== 'welcome' && s.id !== 'finish').map(s => s.id)

export default function FinishSetupCard({ isActive = true }) {
  const { t } = useTranslation()
  const { openOnboarding, state, loadFull } = useContext(OnboardingContext)
  const [hidden, setHidden] = useState(loadDismissed)
  const [full, setFull] = useState(false) // the gate's state carries every fact
  const [opening, setOpening] = useState('')
  const read = useCallback(async () => {
    const r = await loadFull()
    if (r && r.ok) setFull(true)
    else console.error('finish-setup card: onboarding state', r?.message) // the card stays as it was
  }, [loadFull])
  useEffect(() => { if (!hidden) read() }, [hidden, read])
  const wasActive = useRef(isActive)
  useEffect(() => {
    const back = isActive && !wasActive.current
    wasActive.current = isActive
    if (back && !hidden) read()
  }, [isActive, hidden, read])

  if (hidden || !full || !state) return null
  const statuses = statusesOf(state)
  const left = SETUP_STEPS.filter(id => statuses[id] !== 'done')
  if (left.length === 0) return null

  const open = async (id) => {
    if (opening) return
    setOpening(id)
    try { await openOnboarding(id) } finally { setOpening('') }
  }
  const dismiss = () => { saveDismissed(); setHidden(true) }

  return (
    <div className="card" data-testid="finish-setup" style={{ marginBottom: 16 }}>
      <div className="section-header">
        <div className="section-title"><Rocket size={12} /> {t('onboarding.finishCard.title')}</div>
        <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10.5, color: 'var(--text-muted)', marginLeft: 'auto', marginRight: 6 }}>
          {t('onboarding.finishCard.progress', { done: SETUP_STEPS.length - left.length, total: SETUP_STEPS.length })}
        </span>
        <button className="btn btn-ghost btn-icon" onClick={dismiss} aria-label={t('onboarding.finishCard.dismiss')} title={t('onboarding.finishCard.dismiss')}>
          <X size={13} />
        </button>
      </div>
      <ol style={{ listStyle: 'none', margin: 0, padding: 0, display: 'flex', flexDirection: 'column', gap: 2 }}>
        {left.map(id => (
          <li key={id} style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '8px 4px', borderTop: '1px solid var(--border-dim)' }}>
            <Circle size={15} aria-hidden="true" style={{ color: 'var(--text-dim)', flexShrink: 0 }} />
            <div style={{ flex: 1, minWidth: 0, fontSize: 12.5, color: 'var(--text)' }}>
              {railLabel(t, id)}
              <span style={{ marginLeft: 8, fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)' }}>
                {statuses[id] === 'skipped' ? t('onboarding.finishCard.skipped') : t('onboarding.finishCard.notDone')}
              </span>
            </div>
            <button className="btn btn-sm btn-secondary" disabled={!!opening} onClick={() => open(id)}
              aria-label={t('onboarding.finishCard.openStep', { name: railLabel(t, id) })}>
              {t('onboarding.finishCard.open')}
            </button>
          </li>
        ))}
      </ol>
    </div>
  )
}
