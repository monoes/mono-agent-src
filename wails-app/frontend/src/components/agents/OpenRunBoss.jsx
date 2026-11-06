import { useTranslation } from 'react-i18next'
import { Crown } from 'lucide-react'
import { useSessionOrbit } from '../bubbles/useSessionOrbit.js'
import { activeBoss, orbitState } from '../../lib/sessionOrbit.js'
import { useSendToCoder } from '../bubbles/CoderBubbleContext.jsx'

// OpenRunBoss lists the profile's open-ended run's boss while it is open: the session the operator recorded as the boss,
// its state, what it spent, and a link to its board. Nothing renders when no run's boss is open (one run at a time).
export default function OpenRunBoss() {
  const { t } = useTranslation()
  const { sessions } = useSessionOrbit(true)
  const { navigate } = useSendToCoder()
  const boss = activeBoss(sessions)
  if (!boss) return null
  const state = orbitState(boss)
  const spent = Number(boss.spent_usd) || 0
  return (
    <section data-testid="open-run-boss" aria-label={t('agents.boss.title')} style={{ marginTop: 16, padding: 12, border: '1px solid var(--border-active)', borderRadius: 'var(--radius-lg)', display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap' }}>
      <Crown size={16} aria-hidden="true" style={{ color: 'var(--cyan)' }} />
      <div style={{ display: 'flex', flexDirection: 'column', gap: 2, minWidth: 0, flex: 1 }}>
        <span style={{ fontFamily: 'var(--font-mono)', fontSize: 11, fontWeight: 600 }}>{t('agents.boss.title')}</span>
        <span style={{ fontSize: 10, color: 'var(--text-muted)' }}>
          {t('agents.boss.boss')} #{boss.id} · {t('agents.boss.state')}: {t(`bubbles.orbit.state.${state}`)} · {t('agents.boss.spent')}: ${spent.toFixed(2)}
        </span>
      </div>
      <button type="button" className="btn btn-sm" onClick={() => navigate?.('tasks', { sessionId: boss.id })}>{t('agents.boss.open')}</button>
    </section>
  )
}
