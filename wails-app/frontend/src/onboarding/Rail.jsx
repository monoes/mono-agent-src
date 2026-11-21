import { useTranslation } from 'react-i18next'
import { railLabel } from './steps.js'

const Check = () => (
  <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M5 12.5l4.5 4.5L19 7.5" fill="none" stroke="currentColor" strokeWidth="2.4" strokeLinecap="round" strokeLinejoin="round" /></svg>
)
const Skipped = () => (
  <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M6 12h12M14 8l4 4-4 4" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" /></svg>
)

function stateWord(t, status, current) {
  if (current) return t('onboarding.shell.state.current')
  if (status === 'done') return t('onboarding.shell.state.done')
  if (status === 'skipped') return t('onboarding.shell.state.skipped')
  return t('onboarding.shell.state.upcoming')
}

// The progress rail in the header: every step the mode shows, with its real
// status (data-status) and the one the person is on (aria-current). The wire
// fills up to the current step: where the person is, not what is done.
export default function Rail({ steps, statuses, index }) {
  const { t } = useTranslation()
  const n = steps.length
  const half = 50 / n
  const fill = n > 1 ? (index / (n - 1)) * 100 : 0
  return (
    <nav className="onb-journey" aria-label={t('onboarding.shell.progress')}>
      <div className="onb-wire" style={{ left: `${half}%`, right: `${half}%` }}><i style={{ width: `${fill}%` }} /></div>
      <ol style={{ gridTemplateColumns: `repeat(${n}, 1fr)` }}>
        {steps.map((s, i) => {
          const status = statuses[s.id] || 'todo'
          const current = i === index
          const name = railLabel(t, s.id)
          return (
            <li key={s.id} data-step={s.id} data-status={status} className={current ? `${status} current` : status} aria-current={current ? 'step' : undefined}>
              <span className="onb-rail-dot" role="img" aria-label={t('onboarding.shell.railItem', { n: i + 1, name, state: stateWord(t, status, current) })}>
                {current ? i + 1 : status === 'done' ? <Check /> : status === 'skipped' ? <Skipped /> : i + 1}
              </span>
              <span className="onb-rail-lbl" aria-hidden="true">
                {name}
                {status === 'skipped' && !current && <em>{t('onboarding.shell.skipped')}</em>}
              </span>
            </li>
          )
        })}
      </ol>
    </nav>
  )
}
