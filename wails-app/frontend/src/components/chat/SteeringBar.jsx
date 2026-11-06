import { useTranslation } from 'react-i18next'
import { Compass } from 'lucide-react'

const cut = (s, n = 120) => (s.length > n ? `${s.slice(0, n - 1)}…` : s)

// SteeringBar sits above the composer of a chat whose open-ended run is going: what is typed here goes to the boss as
// steering (never an answer, never an approval), what was last sent, and why a send was refused.
export function SteeringBar({ steer }) {
  const { t } = useTranslation()
  return (
    <div data-testid="steering-bar" className="steering-bar" style={{ padding: '6px 12px', fontSize: 10.5, borderTop: '1px solid var(--border)', color: 'var(--text-secondary)', display: 'flex', flexDirection: 'column', gap: 2 }}>
      <span style={{ display: 'flex', alignItems: 'center', gap: 6, fontWeight: 600, color: 'var(--cyan)' }}>
        <Compass size={11} aria-hidden="true" /> {t('steering.banner')}
      </span>
      <span style={{ color: 'var(--text-muted)' }}>{t('steering.note')}</span>
      <span role="status" aria-live="polite">
        {steer.error
          ? <span style={{ color: 'var(--red)' }}>{t('steering.failed', { error: steer.error })}</span>
          : steer.last ? t('steering.sent', { text: cut(steer.last) }) : ''}
      </span>
    </div>
  )
}
