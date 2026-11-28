import { useEffect, useState } from 'react'
import { controlRoomApi, CONTROL_ROOM_CHANGED } from '../../services/controlRoom.js'

const POLL_MS = 30000

// useRunExists asks the control room's probe (the CLI's overview, cached for a few seconds by the binding) whether the
// profile has an open-ended run: true, false, or null while unknown (not asked yet, or the read failed, which is what
// a signed-out window gets). Only a definite answer replaces the last one, like the nav's own probe. A start or a Stop
// in this window (CONTROL_ROOM_CHANGED) asks again at once.
export function useRunExists(enabled, api = controlRoomApi) {
  const [exists, setExists] = useState(null)
  useEffect(() => {
    if (!enabled) { setExists(null); return undefined }
    let live = true
    let timer = null
    let asked = 0 // only the newest question's answer counts
    const read = async () => {
      clearTimeout(timer)
      const mine = ++asked
      const p = await api.probe()
      if (!live || mine !== asked) return
      if (p && !p.error && typeof p.run_exists === 'boolean') setExists(p.run_exists)
      timer = setTimeout(read, POLL_MS)
    }
    read()
    window.addEventListener(CONTROL_ROOM_CHANGED, read)
    return () => { live = false; clearTimeout(timer); window.removeEventListener(CONTROL_ROOM_CHANGED, read) }
  }, [enabled, api])
  return exists
}
