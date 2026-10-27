import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Check, MessageSquare, Pause, Play, Square, Archive, CircleCheck } from 'lucide-react'
import { confirm } from '../ConfirmDialog.jsx'
import BudgetFlag from './BudgetFlag.jsx'
import StateChip from './StateChip.jsx'
import { actionsFor, nearBudget, progress, spendParts } from '../../lib/taskSessionModel.js'

const ICONS = { approve: Check, pause: Pause, resume: Play, stop: Square, close: CircleCheck, archive: Archive }

// The chosen session's header: state, progress, spend, its goal, the actions
// its state offers (Approve plan, Pause, Resume, Stop, Archive, Edit budget)
// and the Board/Plan toggle. Every action is the CLI's call; a refusal shows
// in the alert. Open conversation only shows when the session has one and the
// app can open it.
export default function SessionHeader({ session, view, onView, onAct, onBudget, notice, onClearNotice, onOpenConversation }) {
  const { t } = useTranslation()
  const [editing, setEditing] = useState(false)
  const [usd, setUsd] = useState('')
  const [busy, setBusy] = useState(false)
  const actions = actionsFor(session)
  const { spent, budget } = spendParts(session)
  const run = async (verb) => {
    // Stop is for good (every claim released): ask first.
    if (verb === 'stop' && !(await confirm(t('tasks.session.confirmStop')))) return
    if (verb === 'close' && !(await confirm(t('tasks.session.confirmClose')))) return
    setBusy(true)
    try { await onAct(verb, session.id) } finally { setBusy(false) }
  }
  const saveBudget = async (e) => {
    e.preventDefault()
    const n = Number(usd)
    if (usd.trim() === '' || !Number.isFinite(n) || n < 0) return
    setBusy(true)
    try { const r = await onBudget(session.id, n); if (!r?.error) setEditing(false) } finally { setBusy(false) }
  }
  return (
    <section className="ts-head" aria-label={t('tasks.session.header', { title: session.title })}>
      <div className="ts-head-row">
        <h2 className="ts-title">{session.title}</h2>
        <StateChip session={session} />
        {session.archived && <span className="ts-chip ts-chip--archived">{t('tasks.session.archived')}</span>}
        <span data-testid="ts-head-progress">{t('tasks.session.progress', { done: progress(session) })}</span>
        <span data-testid="ts-head-spend" className={nearBudget(session) ? 'ts-near' : undefined}>
          {budget ? t('tasks.session.spendOf', { spent, budget }) : t('tasks.session.spendNoBudget', { spent })}
        </span>
        <BudgetFlag session={session} />
        <span className="ts-spacer" />
        <div role="group" aria-label={t('tasks.session.view')} className="ts-seg">
          {['board', 'plan'].map(v => (
            <button key={v} type="button" className={`btn btn-ghost btn-sm${view === v ? ' is-on' : ''}`} aria-pressed={view === v} onClick={() => onView(v)}>{t(`tasks.session.view_${v}`)}</button>
          ))}
        </div>
      </div>
      {session.goal && <p className="ts-goal">{session.goal}</p>}
      <div className="ts-head-row ts-actions">
        {actions.filter(a => a !== 'budget').map(a => {
          const Icon = ICONS[a]
          return (
            <button key={a} type="button" disabled={busy} data-testid={`ts-${a}`}
              className={`btn btn-sm ${a === 'approve' ? 'btn-primary' : 'btn-secondary'}`} onClick={() => run(a)}>
              <Icon size={12} aria-hidden="true" /> {t(`tasks.session.action.${a}`)}
            </button>
          )
        })}
        {actions.includes('budget') && !editing && (
          <button type="button" className="btn btn-secondary btn-sm" onClick={() => { setUsd(session.budget_usd > 0 ? String(session.budget_usd) : ''); setEditing(true) }}>{t('tasks.session.action.budget')}</button>
        )}
        {session.conversation_id && onOpenConversation && (
          <button type="button" className="btn btn-ghost btn-sm" onClick={() => onOpenConversation(session.conversation_id)}><MessageSquare size={12} aria-hidden="true" /> {t('tasks.session.action.conversation')}</button>
        )}
      </div>
      {editing && (
        <form className="ts-budget" onSubmit={saveBudget}>
          <label>{t('tasks.session.budgetLabel')} <input className="form-input" type="number" min="0" step="0.01" value={usd} onChange={e => setUsd(e.target.value)} /></label>
          <button className="btn btn-primary btn-sm" type="submit" disabled={busy || usd.trim() === '' || !(Number(usd) >= 0)}>{t('tasks.drawer.save')}</button>
          <button className="btn btn-ghost btn-sm" type="button" onClick={() => setEditing(false)}>{t('tasks.session.cancel')}</button>
          <span className="ts-hint">{t('tasks.session.budgetHint')}</span>
        </form>
      )}
      {notice && <div role="alert" onClick={onClearNotice}>{t('tasks.refused', { error: notice.error })}</div>}
    </section>
  )
}
