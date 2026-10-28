import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import BudgetFlag from './BudgetFlag.jsx'
import StateChip from './StateChip.jsx'
import SessionNew from './SessionNew.jsx'
import SessionRename from './SessionRename.jsx'
import SessionTree from './SessionTree.jsx'
import { hasSessionTree, progress, spendParts } from '../../lib/taskSessionModel.js'

// Personal (today's board) and each session as a tab: state chip, progress,
// spend against the budget. Left and Right move between tabs (roving focus).
// It is always shown (Personal with no session at all), with New session and,
// for a chosen session, Rename. Once a session has a parent the strip is a
// collapsible tree (SessionTree). onCreate({title, goal, parentId}) and
// onRename(id, title) resolve to the CLI's document or {error}.
export default function SessionSwitcher({ sessions, selectedId, onSelect, error = false, onRetry, onCreate, onRename }) {
  const { t } = useTranslation()
  const [creating, setCreating] = useState(false)
  const [renaming, setRenaming] = useState(false)
  useEffect(() => { setRenaming(false) }, [selectedId])
  // A session chosen from elsewhere (a chat's plan card) may sit past the strip's edge: show it.
  useEffect(() => {
    document.querySelector('[data-ts-tab][aria-selected="true"]')?.scrollIntoView?.({ block: 'nearest', inline: 'nearest' })
  }, [selectedId, sessions.length])
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
  const chosen = sessions.find(x => x.id === selectedId)
  const nested = hasSessionTree(sessions)
  const tabs = nested ? <SessionTree sessions={sessions} selectedId={selectedId} onSelect={onSelect} /> : (
    <div role="tablist" aria-label={t('tasks.session.switcher')} className="ts-tabs">
      {tab(null, <span className="ts-tab-title">{t('tasks.session.personal')}</span>, null)}
      {sessions.map(s => {
        const { spent, budget } = spendParts(s)
        return tab(s.id, <span className="ts-tab-title" title={s.title}>{s.title}</span>, (
          <span className="ts-tab-meta">
            <StateChip session={s} />
            <BudgetFlag session={s} />
            {s.created_by && s.created_by !== 'you' && <span className="ts-by" data-testid="ts-creator">{t('tasks.session.by', { who: s.created_by })}</span>}
            <span data-testid="ts-progress">{progress(s)}</span>
            <span data-testid="ts-spend">{budget ? t('tasks.session.spendOf', { spent, budget }) : spent}</span>
          </span>
        ))
      })}
    </div>
  )
  return (
    <div className="ts-switcher">
    {tabs}
    <span className="ts-tools">
      {onRename && chosen && (renaming
        ? <SessionRename session={chosen} onRename={onRename} onDone={() => setRenaming(false)} />
        : <button type="button" className="btn btn-ghost btn-sm" onClick={() => setRenaming(true)}>{t('tasks.session.rename')}</button>)}
      {onCreate && (
        <button
          type="button" className="btn btn-ghost btn-sm" aria-haspopup="dialog" aria-expanded={creating}
          onClick={() => setCreating(v => !v)}
        >{t('tasks.session.new')}</button>
      )}
    </span>
    {error && (
      <span className="ts-list-error" role="alert">
        {t('tasks.session.listError')}
        <button type="button" className="btn btn-ghost btn-sm" onClick={() => onRetry?.()}>{t('tasks.session.retry')}</button>
      </span>
    )}
    {creating && onCreate && (
      <SessionNew
        sessions={sessions.filter(x => !x.archived)} defaultParent={chosen?.id || 0} onCreate={onCreate}
        onClose={(res) => { setCreating(false); if (res?.session?.id) onSelect(res.session.id) }}
      />
    )}
    </div>
  )
}
