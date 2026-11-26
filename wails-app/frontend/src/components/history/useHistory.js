import { useState, useEffect, useRef, useCallback } from 'react'
import { api, subscribeEvent } from '../../services/api.js'
import { PAGE_SIZE, toFilter, mergePage, reconcileRefresh } from '../../lib/historyModel.js'
import i18nOr from '../../lib/i18nOr.js'

const REFRESH_MS = 2000
const QUERY_DEBOUNCE_MS = 300
const TOMB_MS = 30000

// useHistory pages ListHistory for the open drawer. While `open` it refreshes
// the first page when a chat turn finishes or an org run changes status, at
// most once per REFRESH_MS, and once on open.
export function useHistory({ open, kind, status, query, linked }) {
  const [items, setItems] = useState([])
  const [cursor, setCursor] = useState('')
  const [warnings, setWarnings] = useState([])
  const [loading, setLoading] = useState(false)
  const [loadingMore, setLoadingMore] = useState(false)
  const [error, setError] = useState('')
  const [debounced, setDebounced] = useState(query)
  const seq = useRef(0)
  const lastRefresh = useRef(0)
  const timer = useRef(null)
  const cursorRef = useRef('')
  const tombs = useRef(new Map()) // id -> expiry: deleted rows a stale answer must not bring back
  const pages = useRef(1) // pages loaded; a refresh only moves the cursor while it is 1
  cursorRef.current = cursor

  useEffect(() => {
    const id = setTimeout(() => setDebounced(query), QUERY_DEBOUNCE_MS)
    return () => clearTimeout(id)
  }, [query])

  const filter = toFilter({ kind, status, query: debounced, linked })
  const filterKey = JSON.stringify(filter)

  // alive drops rows deleted a moment ago (an answer already in flight may still list them).
  const alive = rows => {
    const now = Date.now()
    for (const [id, until] of tombs.current) if (until < now) tombs.current.delete(id)
    return rows.filter(r => !tombs.current.has(r.id))
  }

  // load replaces the list (first page); a stale answer is dropped.
  const load = useCallback(async ({ silent = false } = {}) => {
    const mine = ++seq.current
    if (!silent) setLoading(true)
    try {
      const page = await api.listHistory('', PAGE_SIZE, JSON.parse(filterKey))
      if (mine !== seq.current) return
      const first = alive(page.items || [])
      setItems(prev => (silent ? reconcileRefresh(alive(prev), first, !!page.next_cursor) : first))
      if (!silent || pages.current <= 1) { setCursor(page.next_cursor || ''); pages.current = 1 }
      setWarnings(page.warnings || [])
      setError('')
    } catch (e) {
      if (mine !== seq.current) return
      if (!silent) setError(String(e?.message || e || i18nOr('ui6PagesMisc.unknown_error')))
    } finally {
      if (mine === seq.current) setLoading(false)
    }
  }, [filterKey])

  const loadMore = useCallback(async () => {
    const c = cursorRef.current
    if (!c || loadingMore) return
    const mine = seq.current
    setLoadingMore(true)
    try {
      const page = await api.listHistory(c, PAGE_SIZE, JSON.parse(filterKey))
      if (mine !== seq.current) return
      setItems(prev => mergePage(prev, alive(page.items || [])))
      setCursor(page.next_cursor || '')
      pages.current += 1
      if (page.warnings?.length) setWarnings(w => [...new Set([...w, ...page.warnings])])
    } catch (e) {
      if (mine === seq.current) setError(String(e?.message || e || i18nOr('ui6PagesMisc.unknown_error')))
    } finally {
      setLoadingMore(false)
    }
  }, [filterKey, loadingMore])

  // First page on open and whenever the filter changes.
  useEffect(() => { if (open) { lastRefresh.current = Date.now(); load() } }, [open, load])

  // Live refresh, throttled to one per REFRESH_MS (trailing call kept).
  useEffect(() => {
    if (!open) return undefined
    const fire = () => {
      const wait = lastRefresh.current + REFRESH_MS - Date.now()
      if (wait <= 0) { lastRefresh.current = Date.now(); load({ silent: true }); return }
      if (timer.current) return
      timer.current = setTimeout(() => { timer.current = null; lastRefresh.current = Date.now(); load({ silent: true }) }, wait)
    }
    const offChat = subscribeEvent('chat:event', ev => { if (ev?.type === 'turn.finished') fire() })
    const offOrg = subscribeEvent('org:runStatus', fire)
    return () => {
      if (typeof offChat === 'function') offChat()
      if (typeof offOrg === 'function') offOrg()
      if (timer.current) { clearTimeout(timer.current); timer.current = null }
    }
  }, [open, load])

  const removeLocal = useCallback(ids => {
    ids.forEach(id => tombs.current.set(id, Date.now() + TOMB_MS))
    const gone = new Set(ids)
    setItems(prev => prev.filter(r => !gone.has(r.id)))
  }, [])

  return { items, cursor, warnings, loading, loadingMore, error, reload: load, loadMore, removeLocal }
}
