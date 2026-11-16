import { useTranslation } from 'react-i18next'
import { Check, X, Hourglass, Pause, Minus, Activity, BellRing, Square, Crown, Home } from 'lucide-react'
import { statusLine } from './OrbitRing.jsx'
import './runroster.css'

const STATE_ICON = { running: Activity, waiting: Hourglass, done: Check, failed: X, paused: Pause, stopped: Square, idle: Minus }

function Card({ c, active, onSelect, icon: Lead }) {
  const { t } = useTranslation()
  const Icon = c.waiting > 0 ? BellRing : STATE_ICON[c.state] || Minus
  return (
    <button type="button" className="roster-card" data-state={c.state} data-active={active ? 'true' : undefined} aria-pressed={active}
      data-testid={`roster-role-${c.id}`} title={c.canOpen ? undefined : t('bubbles.roster.noChat')} onClick={() => onSelect(c)}>
      <span className="roster-mark" aria-hidden="true"><Icon size={11} strokeWidth={2.5} /></span>
      <span className="roster-body">
        <span className="roster-title">{Lead ? <Lead size={10} aria-hidden="true" /> : null}{c.title}</span>
        <span className="roster-meta">{statusLine(t, c)}</span>
      </span>
    </button>
  )
}

// RunRoster is the open-ended run's dedicated space: the front desk (the chat itself), the boss, the planners and the
// executors, each a card. A card opens that role's conversation in the chat area below (onSelect(circle)); the front desk
// card (onSelect(null)) goes back to the chat. activeId is the opened role's session id, null for the front desk.
export function RunRoster({ roster, activeId, onSelect, deskTitle }) {
  const { t } = useTranslation()
  const group = (key, list) => list.length > 0 && (
    <div className="roster-group" role="group" aria-label={t(`bubbles.roster.${key}`)}>
      <span className="roster-label">{t(`bubbles.roster.${key}`)}</span>
      <div className="roster-cards">{list.map(c => <Card key={c.id} c={c} active={activeId === c.id} onSelect={onSelect} />)}</div>
    </div>
  )
  return (
    <section className="roster" data-testid="run-roster" aria-label={t('bubbles.roster.label')}>
      <div className="roster-group" role="group" aria-label={t('bubbles.roster.desk')}>
        <span className="roster-label">{t('bubbles.roster.desk')}</span>
        <div className="roster-cards">
          <button type="button" className="roster-card" aria-pressed={activeId === null} data-active={activeId === null ? 'true' : undefined} data-testid="roster-desk" onClick={() => onSelect(null)}>
            <span className="roster-mark" aria-hidden="true"><Home size={11} /></span>
            <span className="roster-body"><span className="roster-title">{deskTitle}</span></span>
          </button>
        </div>
      </div>
      <div className="roster-group" role="group" aria-label={t('bubbles.roster.boss')}>
        <span className="roster-label">{t('bubbles.roster.boss')}</span>
        <div className="roster-cards"><Card c={roster.boss} active={activeId === roster.boss.id} onSelect={onSelect} icon={Crown} /></div>
      </div>
      {group('planners', roster.planners)}
      {group('executors', roster.executors)}
    </section>
  )
}
