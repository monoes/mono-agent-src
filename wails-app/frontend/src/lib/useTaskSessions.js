// The session list of the Tasks tab, the history of the chosen session, and
// the header actions. The list is read when the page opens, after each
// action, and every LIST_POLL_MS while the page is shown (a session's state,
// progress and spend move with no push event); the chosen session's history
// is read again when its revision moves. A failed read leaves the last list.
import { useCallback, useEffect, useRef, useState } from 'react'
import { sessionsApi } from '../services/taskSessions.js'
import { usePageVisible } from './usePageVisible.js'

const LIST_POLL_MS = 4000

export function useTaskSessions(active, selectedId) {
  const [sessions, setSessions] = useState([])
  const [loaded, setLoaded] = useState(false) // the first list read has answered (well or not)
  const [events, setEvents] = useState([])
  const [listError, setListError] = useState(false) // the latest list read failed
  const [notice, setNotice] = useState(null) // {error, code}
  const seq = useRef(0)
  const shownFor = useRef(null) // the session the events belong to
  const visible = usePageVisible()

  const reload = useCallback(async () => {
    const mine = ++seq.current
    const doc = await sessionsApi.list(false)
    if (mine !== seq.current) return
    setLoaded(true)
    setListError(!!doc?.error)
    if (doc?.error) return
    setSessions(Array.isArray(doc?.sessions) ? doc.sessions : [])
  }, [])

  useEffect(() => {
    if (!active) return undefined
    reload()
    const timer = setInterval(() => { if (!document.hidden) reload() }, LIST_POLL_MS)
    return () => { seq.current++; clearInterval(timer) }
  }, [active, reload])

  useEffect(() => { if (active && visible) reload() }, [active, visible, reload])

  const selected = sessions.find(s => s.id === selectedId) || null
  const rev = selected?.rev
  useEffect(() => {
    // Another session starts with no events (never A's under B); a late
    // answer for a session no longer chosen is dropped.
    if (shownFor.current !== selectedId) { shownFor.current = selectedId; setEvents([]) }
    if (!selectedId) return undefined
    let live = true
    sessionsApi.show(selectedId, 30).then(r => { if (live && !r?.error) setEvents(r?.events || []) })
    return () => { live = false }
  }, [selectedId, rev])

  // act runs a header action (approve, pause, resume, stop, archive) and
  // reports a refusal in `notice`; the list is read again either way.
  const act = useCallback(async (verb, id) => {
    const res = await sessionsApi[verb](id)
    setNotice(res?.error ? { error: res.error, code: res.code } : null)
    await reload()
    return res
  }, [reload])

  const setBudget = useCallback(async (id, usd) => {
    const res = await sessionsApi.budget(id, usd)
    setNotice(res?.error ? { error: res.error, code: res.code } : null)
    await reload()
    return res
  }, [reload])

  return { sessions, loaded, listError, selected, events, notice, clearNotice: () => setNotice(null), act, setBudget, reload }
}
