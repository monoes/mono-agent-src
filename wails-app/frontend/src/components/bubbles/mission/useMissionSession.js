import { useCallback, useEffect, useRef, useState } from 'react'
import { sessionsApi } from '../../../services/taskSessions.js'
import { isTerminal, sessionOf } from '../../../lib/missionModel.js'

const POLL_MS = 3000
const FIND_MS = 6000

// useSessionLive reads one session (TaskSessionShow) and again whenever its
// revision moves (TaskSessionPulse, as the Tasks page does), so a card shows
// the session's real state, never the event's. {session, board, missing,
// reload}; board is read only with withBoard. A session that is over stops
// the polling.
export function useSessionLive(sessionId, { withBoard = false, enabled = true } = {}) {
  const [session, setSession] = useState(null)
  const [board, setBoard] = useState(null)
  const [missing, setMissing] = useState(false)
  const rev = useRef(null)
  const alive = useRef(true)
  const current = useRef(0) // request id: only the newest read of the current session may land
  useEffect(() => { alive.current = true; return () => { alive.current = false } }, [])
  // Another session id starts clean: nothing read for the old one may show
  // or land (the id bump drops reads still in flight).
  useEffect(() => {
    current.current++
    rev.current = null
    setSession(null); setBoard(null); setMissing(false)
  }, [sessionId])

  const reload = useCallback(async () => {
    if (!sessionId) return null
    const mine = ++current.current
    const ok = () => alive.current && mine === current.current
    const doc = await sessionsApi.show(sessionId, 0)
    if (!ok()) return null
    if (doc?.error || !doc?.session) { setMissing(true); return null }
    setMissing(false)
    setSession(doc.session)
    rev.current = doc.session.rev
    if (withBoard && !isTerminal(doc.session)) {
      const b = await sessionsApi.board(sessionId)
      if (ok() && !b?.error) setBoard(b)
    }
    return doc.session
  }, [sessionId, withBoard])

  useEffect(() => {
    if (!enabled || !sessionId) return undefined
    let stopped = false
    reload()
    const timer = setInterval(async () => {
      if (stopped || document.hidden) return
      const p = await sessionsApi.pulse(sessionId)
      if (stopped || typeof p?.rev !== 'number') return
      if (p.rev !== rev.current || (p.state && p.state !== (sessionRef.current?.state))) reload()
    }, POLL_MS)
    return () => { stopped = true; clearInterval(timer) }
  }, [enabled, sessionId, reload])

  const sessionRef = useRef(null)
  sessionRef.current = session
  // Belt and braces for the render between an id change and its reset.
  const mine = session && session.id === sessionId
  return { session: mine ? session : null, board: mine ? board : null, missing, reload }
}

// useConversationSession finds the session a conversation opened
// (conversation_id, from TaskSessions(all)), caches its id, and follows it
// like useSessionLive. bump changes whenever a mission event or a turn
// lands, which asks for a fresh look. Before a session exists the list is
// read every FIND_MS only while enabled.
const cache = new Map() // conversationId -> session id

export function useConversationSession(conversationId, { enabled = true, bump = 0 } = {}) {
  const [id, setId] = useState(() => cache.get(conversationId) || 0)
  useEffect(() => { setId(cache.get(conversationId) || 0) }, [conversationId])
  const find = useCallback(async () => {
    if (!conversationId) return
    const doc = await sessionsApi.list(true)
    if (doc?.error) return
    const s = sessionOf(doc?.sessions, conversationId)
    if (s) { cache.set(conversationId, s.id); setId(s.id) }
  }, [conversationId])
  useEffect(() => {
    if (!enabled || !conversationId) return undefined
    find()
    if (id) return undefined
    const timer = setInterval(() => { if (!document.hidden) find() }, FIND_MS)
    return () => clearInterval(timer)
    // A found id ends the search; bump restarts it (a new plan may be a new session).
  }, [enabled, conversationId, id, bump, find])
  const live = useSessionLive(id, { withBoard: true, enabled: enabled && !!id })
  return { id, ...live }
}

export const forgetConversationSessions = () => cache.clear()
