// Whether the control room is in the nav. The probe is one cheap call (the binding caches it for a few seconds); the
// answer is hidden until it says so. Shown when the mode is available AND a run exists, or when the mode is available
// and the person turned the page on with localStorage doomsday_ui=1. Paused while the window is hidden.
import { useEffect, useState } from 'react'
import { controlRoomApi } from '../services/controlRoom.js'
import { usePageVisible } from './usePageVisible.js'

export const PROBE_POLL_MS = 15000

export function probeShows(probe, uiFlag) {
  return !!probe?.mode_available && (!!probe?.run_exists || !!uiFlag)
}

function uiFlag() {
  try { return localStorage.getItem('doomsday_ui') === '1' } catch { return false }
}

export function useControlRoomProbe(api = controlRoomApi) {
  const visible = usePageVisible()
  const [probe, setProbe] = useState(null)
  useEffect(() => {
    if (!visible) return undefined
    let cancelled = false
    const read = async () => { const p = await api.probe(); if (!cancelled) setProbe(p?.error ? null : p) }
    read()
    const id = setInterval(read, PROBE_POLL_MS)
    return () => { cancelled = true; clearInterval(id) }
  }, [api, visible])
  return { show: probeShows(probe, uiFlag()), running: !!probe?.run_exists }
}
