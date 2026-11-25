// Feeds the orgActivity reducer for the canvas live view (U15).
//   source 'live'  — seeds from the current run's logs, then folds every
//                    `org:event` for this org (the tail OrgsPanel already
//                    keeps open). Duplicate ids from the seed are skipped.
//   source <runId> — replays that past run's `org logs` through the same
//                    reducer; nothing live is applied.
// Returns { state, loading, error, noRun, now } — `now` is the wall clock
// while live and the run's last event time on replay, so "recent" edges mean
// the same thing in both. `noRun` is the CLI's "this org never ran"; the
// first live event clears it.
import { useEffect, useRef, useState } from 'react'
import { api, onOrgEvent } from '../../services/api.js'
import { initialState, applyEvent } from './orgActivity.js'

function logItems(res) {
  if (!res || res.error) return []
  if (Array.isArray(res.items)) return res.items
  if (Array.isArray(res.events)) return res.events
  if (Array.isArray(res)) return res
  return []
}

export default function useOrgActivity({ orgName, nodes, enabled, source = 'live' }) {
  const [state, setState] = useState(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [noRun, setNoRun] = useState(false)
  const [clock, setClock] = useState(() => Date.now())
  const nodesRef = useRef(nodes)
  nodesRef.current = nodes

  useEffect(() => {
    if (!enabled || !orgName) { setState(null); return }
    let cancelled = false
    const isLive = source === 'live'
    let s = initialState(nodesRef.current || [], { org: orgName })
    setState(s)
    setLoading(true)
    setError('')
    setNoRun(false)

    // Live events that arrive while the seed is loading are buffered and
    // applied after it, so ordering matches the bus.
    const buffered = []
    let seeded = false
    const off = isLive
      ? onOrgEvent((payload) => {
        if (cancelled || payload?.orgName !== orgName || !payload.event) return
        if (!seeded) { buffered.push(payload.event); return }
        setNoRun(false)
        setState(prev => applyEvent(prev || initialState(nodesRef.current || [], { org: orgName }), payload.event))
      })
      : () => {}

    api.getOrgLogs(orgName, isLive ? '' : source).then(res => {
      if (cancelled) return
      if (!res || res.error) setError(res?.error || (isLive ? '' : 'Could not load that run.'))
      for (const ev of logItems(res)) s = applyEvent(s, ev)
      for (const ev of buffered) s = applyEvent(s, ev)
      seeded = true
      setState(s)
      setNoRun(!!res?.no_run && buffered.length === 0)
      setLoading(false)
    })

    const tick = isLive ? setInterval(() => setClock(Date.now()), 1000) : null
    return () => {
      cancelled = true
      off()
      if (tick) clearInterval(tick)
    }
  }, [enabled, orgName, source])

  const now = source === 'live' ? clock : (state?.lastTs || 0)
  return { state, loading, error, noRun, now }
}
