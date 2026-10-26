import { useTranslation } from 'react-i18next'
import { waitingOn } from '../../lib/taskSessionModel.js'

// What a session card adds to the personal one: the assignee, blocked and
// verified badges, and for a card that waits for others, which ones.
export default function CardBadges({ task, board }) {
  const { t } = useTranslation()
  const waits = waitingOn(task, board)
  if (!task.assignee && !task.blocked && !task.verified && !waits.length && !task.retries) return null
  return (
    <div className="ts-badges">
      {task.assignee && <span className="ts-badge" data-testid="assignee">{task.assignee}</span>}
      {task.blocked && <span className="ts-badge ts-badge--blocked" data-testid="blocked">{t('tasks.session.blocked')}</span>}
      {task.verified && <span className="ts-badge ts-badge--verified" data-testid="verified">{t('tasks.session.verified')}</span>}
      {task.retries > 0 && <span className="ts-badge" data-testid="retries">{t('tasks.session.retries', { n: task.retries })}</span>}
      {waits.length > 0 && <span className="ts-badge ts-badge--wait" data-testid="dep-hint">{t('tasks.session.waitsFor', { ids: waits.map(i => `#${i}`).join(', ') })}</span>}
    </div>
  )
}
