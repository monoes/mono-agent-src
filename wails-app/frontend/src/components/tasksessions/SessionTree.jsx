import { useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import BudgetFlag from './BudgetFlag.jsx'
import StateChip from './StateChip.jsx'
import { SESSION_PAGE, capSessionRows, progress, sessionTree, spendParts, visibleRows } from '../../lib/taskSessionModel.js'

// The strip as a collapsible tree (treeview pattern) once some session has a
// parent: Personal first, then each root with its children indented. Up and
// Down move between the rows on show, Left collapses (or goes to the parent),
// Right expands (or goes to the first child), Enter chooses. One row is in the
// tab order (the chosen one, else the first).
// ui is {current: {collapsed, refocus}} kept by the page above: choosing a row
// remounts the page (it is keyed by its session), so what the tree remembers
// (the collapsed rows, and that a key press chose the row so the focus comes
// back to it) lives there and not in this component.
export default function SessionTree({ sessions, selectedId, onSelect, ui }) {
  const { t } = useTranslation()
  const own = useRef({ collapsed: new Set(), refocus: false })
  const keep = ui || own
  const [collapsed, setCollapsed] = useState(() => keep.current.collapsed)
  const [focusId, setFocusId] = useState(undefined) // undefined: follow the selection; null: Personal
  const [limit, setLimit] = useState(SESSION_PAGE)
  const all = useMemo(() => visibleRows(sessionTree(sessions), collapsed), [sessions, collapsed])
  const { kept: rows, hidden } = capSessionRows(all, limit, selectedId, (r) => r.session.id)
  const ids = [null, ...rows.map(r => r.session.id)]
  const current = ids.includes(focusId) ? focusId : (ids.includes(selectedId) ? selectedId : null)
  const move = (id) => {
    setFocusId(id)
    requestAnimationFrame(() => document.querySelector('[data-ts-row][tabindex="0"]')?.focus())
  }
  const toggle = (id, on) => setCollapsed(prev => {
    const next = new Set(prev)
    if (on) next.delete(id); else next.add(id)
    keep.current.collapsed = next
    return next
  })
  useEffect(() => {
    if (!keep.current.refocus) return
    keep.current.refocus = false
    document.querySelector('[data-ts-row][tabindex="0"]')?.focus()
  }, [keep])
  const onKey = (e, row) => {
    const at = ids.indexOf(row ? row.session.id : null)
    switch (e.key) {
      case 'ArrowDown': e.preventDefault(); move(ids[Math.min(at + 1, ids.length - 1)]); break
      case 'ArrowUp': e.preventDefault(); move(ids[Math.max(at - 1, 0)]); break
      case 'ArrowRight':
        if (!row?.hasChildren) break
        e.preventDefault()
        if (!row.expanded) toggle(row.session.id, true)
        else move(ids[at + 1])
        break
      case 'ArrowLeft':
        if (!row) break
        e.preventDefault()
        if (row.expanded) toggle(row.session.id, false)
        else if (row.parentId) move(row.parentId)
        break
      case 'Enter': case ' ': e.preventDefault(); keep.current.refocus = true; onSelect(row ? row.session.id : null); break
      default:
    }
  }
  const item = (row) => {
    const s = row?.session
    const id = s ? s.id : null
    const { spent, budget } = s ? spendParts(s) : {}
    return (
      <li
        key={id ?? 'personal'} role="treeitem" data-ts-row tabIndex={current === id ? 0 : -1}
        aria-level={row ? row.depth : 1} aria-selected={selectedId === id}
        aria-expanded={row?.hasChildren ? row.expanded : undefined}
        className={`ts-row${selectedId === id ? ' is-selected' : ''}`}
        style={{ paddingInlineStart: 8 + (row ? (row.depth - 1) * 16 : 0) }}
        onClick={() => onSelect(id)} onKeyDown={e => onKey(e, row)} onFocus={() => setFocusId(id)}
      >
        {row?.hasChildren ? (
          <button
            type="button" tabIndex={-1} className="ts-row-twisty" aria-hidden="true"
            onClick={e => { e.stopPropagation(); toggle(id, !row.expanded) }}
          >{row.expanded ? '▾' : '▸'}</button>
        ) : <span className="ts-row-twisty" aria-hidden="true" />}
        <span className="ts-tab-title" title={s?.title}>{s ? s.title : t('tasks.session.personal')}</span>
        {s && (
          <span className="ts-tab-meta">
            <StateChip session={s} />
            <BudgetFlag session={s} />
            {s.created_by && s.created_by !== 'you' && <span className="ts-by" data-testid="ts-creator">{t('tasks.session.by', { who: s.created_by })}</span>}
            <span data-testid="ts-progress">{progress(s)}</span>
            <span data-testid="ts-spend">{budget ? t('tasks.session.spendOf', { spent, budget }) : spent}</span>
          </span>
        )}
      </li>
    )
  }
  return (
    <>
      <ul role="tree" aria-label={t('tasks.session.switcher')} className="ts-tree">
        {item(null)}
        {rows.map(item)}
      </ul>
      {hidden > 0 && (
        <button type="button" className="btn btn-ghost btn-sm tb-more" onClick={() => setLimit(l => l + SESSION_PAGE)}>
          {t('tasks.session.sessionsShowMore', { count: Math.min(SESSION_PAGE, hidden), left: hidden })}
        </button>
      )}
    </>
  )
}
