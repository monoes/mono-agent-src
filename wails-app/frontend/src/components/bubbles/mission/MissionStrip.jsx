import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Pause, Play, Square } from 'lucide-react'
import { confirm } from '../../ConfirmDialog.jsx'
import { sessionsApi } from '../../../services/taskSessions.js'
import BudgetFlag from '../../tasksessions/BudgetFlag.jsx'
import StateChip from '../../tasksessions/StateChip.jsx'
import { nearBudget, progress, spendParts } from '../../../lib/taskSessionModel.js'
import { stripActions, stripFor, workingOn } from '../../../lib/missionModel.js'
import { useConversationSession } from './useMissionSession.js'
import './mission.css'

const ICONS = { pause: Pause, resume: Play, stop: Square }

// The mission strip above the composer: while this conversation's session is
// not over it shows phase, progress, spend against budget, the tasks in
// progress with their workers, and Pause / Resume / Stop. Once the report
// arrived (reported) the finished session reads Done. Another
// conversation's session never shows here.
export default function MissionStrip({ conversationId, enabled = true, bump = 0, reported = false }) {
  const { t } = useTranslation()
  const { session, board, reload } = useConversationSession(conversationId, { enabled, bump })
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  if (!stripFor(session, reported) || session.conversation_id !== conversationId) return null
  const { spent, budget } = spendParts(session)
  const working = session.state === 'running' ? workingOn(board) : []
  const run = async (verb) => {
    if (verb === 'stop' && !(await confirm(t('tasks.session.confirmStop')))) return
    setBusy(true)
    try {
      const res = await sessionsApi[verb](session.id)
      setError(res?.error || '')
      await reload()
    } finally { setBusy(false) }
  }
  return (
    <section className="ms-strip" data-testid="mission-strip" aria-label={t('bubbles.mission.stripLabel', { title: session.title })}>
      <div className="ms-strip-row">
        <strong className="ms-strip-title">{session.title}</strong>
        <StateChip session={session} />
        <span data-testid="mission-strip-progress">{t('tasks.session.progress', { done: progress(session) })}</span>
        <span data-testid="mission-strip-spend" className={nearBudget(session) ? 'ts-near' : undefined}>
          {budget ? t('tasks.session.spendOf', { spent, budget }) : t('tasks.session.spendNoBudget', { spent })}
        </span>
        <BudgetFlag session={session} />
        <span style={{ flex: 1 }} />
        {stripActions(session).map(a => {
          const Icon = ICONS[a]
          return <button key={a} type="button" className="btn btn-ghost btn-sm" data-testid={`mission-${a}`} disabled={busy} onClick={() => run(a)}><Icon size={11} aria-hidden="true" /> {t(`tasks.session.action.${a}`)}</button>
        })}
      </div>
      {working.length > 0 && (
        <ul className="ms-working" data-testid="mission-working">
          {working.map(w => <li key={w.id}>{w.title}{w.worker ? <span className="ms-worker"> {w.worker}</span> : null}</li>)}
        </ul>
      )}
      {error && <p className="ms-error" role="alert">{error}</p>}
    </section>
  )
}
