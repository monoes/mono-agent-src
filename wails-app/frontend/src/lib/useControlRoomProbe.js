// Whether the control room is in the nav. The probe is one cheap call (the binding caches it for a few seconds); the
// answer is hidden until it says so. Shown when the mode is available AND a run exists, or when the mode is available
// and the person turned the page on with localStorage doomsday_ui=1. Paused while the window is hidden.
import { useEffect, useRef, useState } from 'react'
import { controlRoomApi } from '../services/controlRoom.js'
import { usePageVisible } from './usePageVisible.js'

export const PROBE_POLL_MS = 15000       // a run exists: the nav chip must follow it
export const PROBE_IDLE_POLL_MS = 60000  // the last good answer said off / no run: nothing to follow

export const probeDelay = (probe) => (probe?.run_exists ? PROBE_POLL_MS : PROBE_IDLE_POLL_MS)

export function probeShows(probe, uiFlag) {
  return !!probe?.mode_available && (!!probe?.run_exists || !!uiFlag)
}

function uiFlag() {
  try { return localStorage.getItem('doomsday_ui') === '1' } catch { return false }
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
    const read = async () => {
      const p = await api.probe()
      if (cancelled) return
      if (!p?.error) { last.current = p; setProbe(p) }
      timer = setTimeout(read, probeDelay(last.current))
    }
    read()
    return () => { cancelled = true; clearTimeout(timer) }
  }, [api, visible])
  return { show: probeShows(probe, uiFlag()), running: !!probe?.run_exists }
}
