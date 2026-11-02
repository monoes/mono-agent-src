import { useCallback, useEffect, useRef, useState } from 'react'
import { sessionsApi } from '../../services/taskSessions.js'
import { usePageVisibleRef } from '../../lib/usePageVisible.js'

const REFRESH_MS = 15000

// useSessionOrbit reads the profile's task sessions (the tree the orbit is drawn
// from) while enabled, every 15 s and when the window comes back. A failed read
// keeps the last list: the orbit never blinks away over a hiccup.
export function useSessionOrbit(enabled) {
  const [sessions, setSessions] = useState([])
  const visible = usePageVisibleRef()
  const epoch = useRef(0)
  const read = useCallback(async () => {
    const mine = ++epoch.current
    const res = await sessionsApi.list(false)
    if (mine !== epoch.current || res?.error || !Array.isArray(res?.sessions)) return
    setSessions(res.sessions)
  }, [])
  useEffect(() => {
    if (!enabled) return undefined
    read()
    const timer = setInterval(() => { if (visible.current) read() }, REFRESH_MS)
    return () => { clearInterval(timer); epoch.current++ }
  }, [enabled, read, visible])
  return { sessions, refresh: read }
}
