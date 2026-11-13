import { useEffect, useState } from 'react'
import { controlRoomApi } from '../../services/controlRoom.js'

const POLL_MS = 30000

// useRunUnbound reads `doomsday status` while the profile has an open-ended run and returns its warning
// ("running without peer-credential binding: <reason>") or ''. The CLI decides; this only shows it.
export function useRunUnbound(runExists, api = controlRoomApi) {
  const [warning, setWarning] = useState('')
  useEffect(() => {
    if (runExists !== true) { setWarning(''); return undefined }
    let live = true
    let timer = null
    const read = async () => {
      const s = await api.status()
      if (!live) return
      if (s && !s.error && typeof s.active === 'boolean') setWarning(s.active && s.warning ? s.warning : '')
      timer = setTimeout(read, POLL_MS)
    }
    read()
    return () => { live = false; clearTimeout(timer) }
  }, [runExists, api])
  return warning
}
