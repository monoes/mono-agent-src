import { useState, useEffect, useRef, useCallback } from 'react'
import { useTranslation } from 'react-i18next'
import { X, Plus, Search, Trash2, Loader2 } from 'lucide-react'
import { confirm } from '../ConfirmDialog.jsx'
import { api } from '../../services/api.js'
import { useSendToCoder } from '../bubbles/CoderBubbleContext.jsx'
import { useHistory } from './useHistory.js'
import { onHistoryRequest } from './historyBus.js'
import HistoryRow from './HistoryRow.jsx'
import { useMissionBadges } from './useMissionBadges.js'
import {
  KIND_FILTERS, STATUS_FILTERS, bubbleKeyOf, liveStatus, deleteOutcome, chatIdsOnly, toFilter, orgRunOpening,
} from '../../lib/historyModel.js'
import './history.css'


// HistoryPanel is the right-side drawer listing every chat and org run. It is
// a non-modal dialog (role=dialog, aria-modal=false): the page behind stays
// usable, so there is no scrim and no focus trap; focus moves in on open,
// returns to the opener on close, and Escape closes. store is the coder
// bubble store (useCoderBubbles): rows open as bubbles, and an open bubble's
// live summary overlays its row.
export default function HistoryPanel({ store, onNavigate }) {
  const { t } = useTranslation()
  const { openDraft } = useSendToCoder()
  const [open, setOpen] = useState(false)
  const [kind, setKind] = useState('')
  const [status, setStatus] = useState('')
  const [linked, setLinked] = useState(false)
  const [query, setQuery] = useState('')
  const [selected, setSelected] = useState(() => new Set())
  const [notice, setNotice] = useState(null) // { tone, text }
  const [busy, setBusy] = useState(false)
  const drawer = useRef(null)
  const opener = useRef(null)
  const sentinel = useRef(null)

  const h = useHistory({ open, kind, status, query, linked })
  const items = h.items
  const missionBadges = useMissionBadges(open, items.filter(r => r.kind !== 'org').map(r => r.id))

  useEffect(() => onHistoryRequest(mode => {
    setOpen(prev => {
      if (mode === 'toggle' && prev) return false
      if (!prev) { opener.current = document.activeElement; setNotice(null) }
      return true
    })
  }), [])

  const close = useCallback(() => {
    setOpen(false)
    const el = opener.current
    opener.current = null
    // WebKit doesn't focus a button on click, so the opener can be <body>:
    // fall back to the status-bar toggle.
    const usable = el && el !== document.body && el !== document.documentElement && document.contains(el)
    const target = usable ? el : document.querySelector('[data-testid="history-status-toggle"]')
    if (target && typeof target.focus === 'function') setTimeout(() => target.focus(), 0)
  }, [])

  useEffect(() => { if (open) drawer.current?.querySelector('input[type="search"]')?.focus() }, [open])

  // Escape closes while focus is inside the drawer or nowhere (after a
  // confirm closes focus falls to <body>); a modal on top (the confirm) and
  // other surfaces that own focus (an expanded bubble) keep their own Escape.
  useEffect(() => {
    if (!open) return undefined
    const onKey = (e) => {
      if (e.key !== 'Escape' || e.defaultPrevented) return
      if (document.querySelector('[aria-modal="true"]')) return
      const inside = drawer.current?.contains(e.target)
      if (inside || e.target === document.body || e.target === document.documentElement) close()
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [open, close])

  // Infinite scroll: load the next page when the end of the list is near.
  useEffect(() => {
    const el = sentinel.current
    if (!open || !el || !h.cursor || typeof IntersectionObserver === 'undefined') return undefined
    const io = new IntersectionObserver(entries => { if (entries.some(e => e.isIntersecting)) h.loadMore() })
    io.observe(el)
    return () => io.disconnect()
  }, [open, h.cursor, h.loadMore, items.length]) // eslint-disable-line react-hooks/exhaustive-deps

  const toggle = useCallback(id => setSelected(prev => {
    const next = new Set(prev)
    if (next.has(id)) next.delete(id); else next.add(id)
    return next
  }), [])

  const openRow = async (row) => {
    if (row.kind === 'org') {
      // A circle is one run: the live one opens the org's live bubble, an
      // older one a read-only bubble for that run.
      const st = await Promise.resolve(api.getOrgStatus(row.orgName)).catch(() => null)
      const opening = orgRunOpening(row, st)
      if (opening) store.openOrg(row.orgName, opening)
      else onNavigate?.('orgs', { org: row.orgName, run: row.runId || undefined })
    } else {
      // The whole row: the pane takes access, mode and the rest from it.
      store.openConversation(row)
    }
    close()
  }

  const openInOrgs = (row) => { onNavigate?.('orgs', { org: row.orgName, run: row.runId || undefined }); close() }

  const openApplication = (id) => { onNavigate?.('applications', { application: id }); close() }

  const newChat = () => {
    // TODO: call openNewChat() from CoderBubbleContext once the pane worker lands it.
    openDraft?.()
    close()
  }

  const runDelete = async (ids) => {
    setBusy(true)
    try {
      const out = deleteOutcome(await api.deleteChatConversations(ids))
      h.removeLocal(out.deleted)
      setSelected(prev => { const n = new Set(prev); out.deleted.forEach(i => n.delete(i)); return n })
      const parts = []
      if (out.deleted.length) parts.push(t('history.deleted', { count: out.deleted.length }))
      if (out.active.length) parts.push(t('history.stillActive', { count: out.active.length }))
      if (out.failed.length) parts.push(t('history.deleteFailed', { count: out.failed.length, message: out.failed[0].message }))
      setNotice({ tone: out.active.length || out.failed.length ? 'warn' : 'ok', text: parts.join(' ') })
      if (out.deleted.length) h.reload({ silent: true })
    } catch (e) {
      setNotice({ tone: 'warn', text: t('history.deleteFailed', { count: ids.length, message: String(e?.message || e) }) })
    } finally {
      setBusy(false)
    }
  }

  const deleteSelected = async () => {
    const ids = chatIdsOnly(h.items.filter(r => selected.has(r.id)))
    if (!ids.length) return
    const ok = await confirm(t('history.confirmDeleteBody', { count: ids.length }),
      { title: t('history.confirmDeleteTitle'), confirmLabel: t('history.confirmDeleteAction'), danger: true })
    if (ok) await runDelete(ids)
  }

  // Clear all chats reads every chat id (kind=chat only), confirms with the
  // count, then deletes just those: org runs are never part of it.
  const clearAll = async () => {
    setBusy(true)
    let ids = []
    try {
      let cursor = ''
      do {
        const page = await api.listHistory(cursor, 100, toFilter({ kind: 'chat' }))
        ids = ids.concat(chatIdsOnly(page.items || []))
        cursor = page.next_cursor || ''
      } while (cursor)
    } catch (e) {
      setBusy(false)
      setNotice({ tone: 'warn', text: t('history.deleteFailed', { count: 0, message: String(e?.message || e) }) })
      return
    }
    setBusy(false)
    if (!ids.length) { setNotice({ tone: 'ok', text: t('history.clearAllNone') }); return }
    const ok = await confirm(t('history.clearAllBody', { count: ids.length }),
      { title: t('history.clearAllTitle'), confirmLabel: t('history.clearAllAction'), danger: true })
    if (ok) await runDelete(ids)
  }

  if (!open) return null

  const filtered = !!(kind || status || linked || query.trim())
  const showEmpty = !h.loading && !h.error && items.length === 0 && !h.cursor
  return (
    <aside
      ref={drawer}
      className="hist-drawer"
      role="dialog"
      aria-modal="false"
      aria-labelledby="hist-title"
      data-testid="history-panel"
    >
      <header className="hist-head">
        <h2 id="hist-title">{t('history.title')}</h2>
        <button type="button" className="hist-btn" onClick={newChat}><Plus size={12} aria-hidden="true" />{t('history.newChat')}</button>
        <button type="button" className="hist-icon-btn" onClick={close} aria-label={t('history.close')} title={t('history.close')}><X size={14} aria-hidden="true" /></button>
      </header>

      <div className="hist-tools">
        <label className="hist-search">
          <Search size={12} aria-hidden="true" />
          <input type="search" value={query} onChange={e => setQuery(e.target.value)} placeholder={t('history.searchPlaceholder')} aria-label={t('history.search')} />
        </label>
        <div className="hist-chips" role="group" aria-label={t('history.filterKind')}>
          <button type="button" className="hist-chip" aria-pressed={kind === ''} onClick={() => setKind('')}>{t('history.kindAll')}</button>
          {KIND_FILTERS.map(k => (
            <button key={k} type="button" className="hist-chip" aria-pressed={kind === k} onClick={() => setKind(kind === k ? '' : k)}>
              {t(k === 'chat' ? 'history.kindChat' : 'history.kindOrg')}
            </button>
          ))}
          <button type="button" className="hist-chip" aria-pressed={linked} onClick={() => setLinked(v => !v)}>{t('history.linked')}</button>
          <select className="hist-select" value={status} onChange={e => setStatus(e.target.value)} aria-label={t('history.statusLabel')}>
            <option value="">{t('history.statusAny')}</option>
            {STATUS_FILTERS.map(s => <option key={s} value={s}>{t(`history.status_${s}`)}</option>)}
          </select>
        </div>
        <div className="hist-actions">
          {selected.size > 0 ? (
            <>
              <span className="hist-count" aria-live="polite">{t('history.selected', { count: selected.size })}</span>
              <button type="button" className="hist-btn danger" disabled={busy} onClick={deleteSelected}><Trash2 size={12} aria-hidden="true" />{t('history.deleteSelected')}</button>
              <button type="button" className="hist-btn" onClick={() => setSelected(new Set())}>{t('history.clearSelection')}</button>
            </>
          ) : (
            <button type="button" className="hist-btn" disabled={busy} onClick={clearAll}><Trash2 size={12} aria-hidden="true" />{t('history.clearAll')}</button>
          )}
        </div>
      </div>

      {h.warnings.length > 0 && (
        <p className="hist-banner" role="status">{t('history.warning', { count: h.warnings.length, names: h.warnings.join(', ') })}</p>
      )}
      {notice && (
        <p className="hist-banner" data-tone={notice.tone} role="status">
          {notice.text}
          <button type="button" className="hist-link" onClick={() => setNotice(null)}>{t('history.dismiss')}</button>
        </p>
      )}

      <div className="hist-scroll">
        {h.loading && items.length === 0 && (
          <ul className="hist-skeleton" aria-label={t('history.loading')} aria-busy="true">
            {[0, 1, 2, 3, 4].map(i => <li key={i} className="hist-skel-row" aria-hidden="true" />)}
          </ul>
        )}
        {h.error && (
          <div className="hist-state" role="alert">
            <p>{t('history.error')}</p>
            <details><summary>{t('history.details')}</summary><pre className="hist-pre">{h.error}</pre></details>
            <button type="button" className="hist-btn" onClick={() => h.reload()}>{t('history.retry')}</button>
          </div>
        )}
        {showEmpty && (
          <div className="hist-state">
            <p><strong>{filtered ? t('history.emptyFiltered') : t('history.empty')}</strong></p>
            {!filtered && <p className="hist-muted">{t('history.emptyHint')}</p>}
            {!filtered && <button type="button" className="hist-btn" onClick={newChat}><Plus size={12} aria-hidden="true" />{t('history.newChat')}</button>}
          </div>
        )}
        {items.length > 0 && (
          <ul className="hist-list" aria-label={t('history.list')}>
            {items.map(row => {
              const key = bubbleKeyOf(row)
              // A live bubble whose run id has not arrived is still org:<name>.
              const isOpen = (store?.bubbles || []).some(b => b.key === key ||
                (row.kind === 'org' && row.status === 'running' && b.key === `org:${row.orgName}`))
              return (
                <HistoryRow
                  key={`${row.kind}:${row.id}`}
                  row={row}
                  mission={missionBadges[row.id]}
                  live={liveStatus(row, isOpen ? store.summaryOf(store.bubbles.some(b => b.key === key) ? key : `org:${row.orgName}`) : null)}
                  selected={selected.has(row.id)}
                  onToggle={toggle}
                  onOpen={openRow}
                  onOpenOrgs={openInOrgs}
                  onOpenApplication={openApplication}
                />
              )
            })}
          </ul>
        )}
        {h.cursor && (
          <div ref={sentinel} className="hist-more">
            <button type="button" className="hist-btn" onClick={h.loadMore} disabled={h.loadingMore}>
              {h.loadingMore ? <><Loader2 size={12} className="hist-spin" aria-hidden="true" />{t('history.loadingMore')}</> : t('history.loadMore')}
            </button>
          </div>
        )}
      </div>
    </aside>
  )
}
