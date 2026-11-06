// Whether the control room is in the nav. The probe is one cheap call (the binding caches it for a few seconds); the
// answer is hidden until it says so. Shown whenever the probe says the mode is available (a run is not required);
// `running` only drives the nav chip. Paused while the window is hidden.
import { useEffect, useRef, useState } from 'react'
import { controlRoomApi } from '../services/controlRoom.js'
import { usePageVisible } from './usePageVisible.js'

export const PROBE_POLL_MS = 15000       // a run exists: the nav chip must follow it
export const PROBE_IDLE_POLL_MS = 60000  // the last good answer said off / no run: nothing to follow

// After a failed probe the next try comes sooner (5 s, 15 s), then the normal cadence: a first failure must not hide
// the nav item for a whole idle period while a run may be live.
export const PROBE_RETRY_MS = [5000, 15000]

export const CONTROL_ROOM_CHANGED = 'monoagent:controlroom-changed'

export const probeDelay = (probe) => (probe?.run_exists ? PROBE_POLL_MS : PROBE_IDLE_POLL_MS)

export function probeShows(probe) {
  return !!probe?.mode_available
}

// A failed probe keeps the last good answer, so the nav item (and the open page) stay while a run may be active.
// One request chain, never overlapping; none while the window is hidden. The only CLI read behind the probe is the
// overview itself (there is no cheaper availability verb), so the idle period is what keeps the cost down.
export function useControlRoomProbe(api = controlRoomApi) {
  const visible = usePageVisible()
  const [probe, setProbe] = useState(null)
  const last = useRef(null)
  useEffect(() => {
    if (!visible) return undefined
    let cancelled = false
    let timer = null
    let failures = 0
    let inflight = false
    let again = false // a change was announced while a read was in flight: one more read after it
    const read = async () => {
      inflight = true
      const p = await api.probe()
      inflight = false
      if (cancelled) return
      if (again) { again = false; failures = 0; read(); return }
      if (!p?.error) { failures = 0; last.current = p; setProbe(p) } else failures++
      const retry = failures > 0 ? PROBE_RETRY_MS[failures - 1] : undefined
      timer = setTimeout(read, retry ?? probeDelay(last.current))
    }
    read()
    // A start or a stop says so: ask again now instead of at the next idle poll.
    // The one chain stays one: while a read is in flight the announcement is remembered, not started beside it.
    const changed = () => { if (inflight) { again = true; return } clearTimeout(timer); read() }
    window.addEventListener(CONTROL_ROOM_CHANGED, changed)
    return () => { cancelled = true; clearTimeout(timer); window.removeEventListener(CONTROL_ROOM_CHANGED, changed) }
  }, [api, visible])
  return { show: probeShows(probe), running: !!probe?.run_exists }
}
