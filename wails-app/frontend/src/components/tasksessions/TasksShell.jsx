import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useTaskSessions } from '../../lib/useTaskSessions.js'
import { useReportTarget } from '../../lib/useReportTarget.js'
import { selectionAfter } from '../../lib/taskSessionModel.js'
import { useSendToCoder } from '../bubbles/CoderBubbleContext.jsx'
import SessionSwitcher from './SessionSwitcher.jsx'
import SessionHeader from './SessionHeader.jsx'
import PlanView from './PlanView.jsx'

// The Tasks tab with its sessions: the switcher on top, and under it one
// board page (Board, the page of Tasks.jsx) for Personal or for the chosen
// session. The page is keyed by its session, so switching remounts it and no
// card of one board is ever shown on another. Personal passes nothing extra:
// it is the board the tab always was.
export default function TasksShell({ isActive = true, Board, sessionHint = null, reportHint = false }) {
  // sessionHint: a session another page (a chat's plan card) asks to show;
  // reportHint: it also asks for the drawer of the task holding its final report.
  const { t } = useTranslation()
  const [selectedId, setSelectedId] = useState(sessionHint)
  const [view, setView] = useState('board')
  const s = useTaskSessions(isActive, selectedId)
  const { openDraft } = useSendToCoder() || {}
  useEffect(() => { if (sessionHint) { setSelectedId(sessionHint); setView('board') } }, [sessionHint])
  // A session that is archived or gone leaves the list: fall back to Personal.
  useEffect(() => {
    if (selectedId && s.sessions.length && selectionAfter(selectedId, s.sessions) === null) setSelectedId(null)
  }, [selectedId, s.sessions])
  const choose = (id) => { setSelectedId(id); setView('board') }
  const session = s.selected
  const found = useReportTarget(sessionHint, reportHint)
  // A hint for another session than the one on show opens nothing here.
  const openTask = found && found.sessionId === selectedId ? found.taskId : null

  const switcher = <SessionSwitcher sessions={s.sessions} selectedId={selectedId} onSelect={choose} error={s.listError} onRetry={s.reload} />
  // A hinted session is the selection from the first paint: while the list loads the page says so, never Personal.
  if (selectedId && !session && !s.loaded) {
    return <div className="page" style={{ padding: 16 }} data-testid="ts-loading">{t('tasks.loading')}</div>
  }
  if (!selectedId || !session) {
    return <Board key="personal" isActive={isActive} switcher={switcher} />
  }
  const header = (
    <SessionHeader
      session={session} view={view} onView={setView} onAct={s.act} onBudget={s.setBudget}
      notice={s.notice} onClearNotice={s.clearNotice}
      onOpenConversation={openDraft ? (id) => openDraft({ conversationId: id }) : null}
    />
  )
  return (
    <Board
      key={`session-${selectedId}`} isActive={isActive} switcher={switcher}
      openTaskId={openTask} session={session} sessionEvents={s.events} sessionHeader={header}
      renderPlan={view === 'plan' ? ({ openTask }) => <PlanView sessionId={selectedId} rev={session.rev} onOpen={openTask} /> : null}
    />
  )
}
