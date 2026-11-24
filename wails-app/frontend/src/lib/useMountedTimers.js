import { useCallback, useEffect, useMemo, useRef } from 'react'

// Timers started from event handlers: every one is cleared when the
// component unmounts, so a late callback never sets state on a dead tree.
export function useMountedTimers() {
  const ids = useRef(new Set())
  useEffect(() => () => {
    ids.current.forEach(({ kind, id }) => (kind === 'i' ? clearInterval(id) : clearTimeout(id)))
    ids.current.clear()
  }, [])
  const timeout = useCallback((fn, ms) => {
    const entry = { kind: 't', id: null }
    entry.id = setTimeout(() => { ids.current.delete(entry); fn() }, ms)
    ids.current.add(entry)
    return entry
  }, [])
  const interval = useCallback((fn, ms) => {
    const entry = { kind: 'i', id: setInterval(fn, ms) }
    ids.current.add(entry)
    return entry
  }, [])
  const clear = useCallback((entry) => {
    if (!entry) return
    if (entry.kind === 'i') clearInterval(entry.id); else clearTimeout(entry.id)
    ids.current.delete(entry)
  }, [])
  return useMemo(() => ({ timeout, interval, clear }), [timeout, interval, clear])
}
