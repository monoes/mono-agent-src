import { useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import BudgetFlag from './BudgetFlag.jsx'
import StateChip from './StateChip.jsx'
import { progress, spendParts } from '../../lib/taskSessionModel.js'

// Personal (today's board) and each session as a tab: state chip, progress,
// spend against the budget. Left and Right move between tabs (roving focus).
export default function SessionSwitcher({ sessions, selectedId, onSelect, error = false, onRetry }) {
  const { t } = useTranslation()
  // A session chosen from elsewhere (a chat's plan card) may sit past the strip's edge: show it.
  useEffect(() => {
    document.querySelector('[data-ts-tab][aria-selected="true"]')?.scrollIntoView?.({ block: 'nearest', inline: 'nearest' })
  }, [selectedId, sessions.length])
  if (!sessions.length && !error) return null
  const ids = [null, ...sessions.map(s => s.id)]
  const onKey = (e, id) => {
    const step = e.key === 'ArrowRight' ? 1 : e.key === 'ArrowLeft' ? -1 : 0
    if (!step) return
    e.preventDefault()
    const next = ids[(ids.indexOf(id) + step + ids.length) % ids.length]
    onSelect(next)
    requestAnimationFrame(() => document.querySelector('[data-ts-tab][aria-selected="true"]')?.focus())
  }
  const tab = (id, label, extra) => (
    <button
      key={id ?? 'personal'} type="button" role="tab" data-ts-tab
      aria-selected={selectedId === id} tabIndex={selectedId === id ? 0 : -1}
      className={`ts-tab${selectedId === id ? ' is-selected' : ''}`}
      onClick={() => onSelect(id)} onKeyDown={e => onKey(e, id)}
    >{label}{extra}</button>
  )
  return (
    <div className="ts-switcher">
    <div role="tablist" aria-label={t('tasks.session.switcher')} className="ts-tabs">
      {tab(null, <span className="ts-tab-title">{t('tasks.session.personal')}</span>, null)}
      {sessions.map(s => {
        const { spent, budget } = spendParts(s)
        return tab(s.id, <span className="ts-tab-title" title={s.title}>{s.title}</span>, (
          <span className="ts-tab-meta">
            <StateChip session={s} />
            <BudgetFlag session={s} />
            <span data-testid="ts-progress">{progress(s)}</span>
            <span data-testid="ts-spend">{budget ? t('tasks.session.spendOf', { spent, budget }) : spent}</span>
          </span>
        ))
      })}
    </div>
    {error && (
      <span className="ts-list-error" role="alert">
        {t('tasks.session.listError')}
        <button type="button" className="btn btn-ghost btn-sm" onClick={() => onRetry?.()}>{t('tasks.session.retry')}</button>
      </span>
    )}
    </div>
  )
}
