import { useEffect, useState } from 'react'
import { controlRoomApi } from '../../services/controlRoom.js'

const POLL_MS = 30000
const PREFIX = 'running without peer-credential binding: '
const UNKNOWN = 'peer-credential binding status of the open run is unknown: '

// unboundText shows the CLI's warning in the app's language: the reason is the CLI's, the sentence around it is translated.
export function unboundText(t, warning) {
  if (warning && warning.startsWith(PREFIX)) return t('openStart.unbound', { reason: warning.slice(PREFIX.length) })
  if (warning && warning.startsWith(UNKNOWN)) return t('openStart.unboundStatusUnknown', { reason: warning.slice(UNKNOWN.length) })
  return warning
}

// useRunUnbound reads `doomsday status` while the profile has an open-ended run and returns {warning, unknown}: the CLI's warning
// ("running without peer-credential binding: <reason>") or '', and unknown=true when the status could not be read (the person is told
// the binding status is unknown, never nothing). The CLI decides; this only shows it.
export function useRunUnbound(runExists, api = controlRoomApi) {
  const [state, setState] = useState({ warning: '', unknown: false })
  useEffect(() => {
    if (runExists !== true) { setState({ warning: '', unknown: false }); return undefined }
    let live = true
    let timer = null
    const read = async () => {
      let s = null
      try { s = await api.status() } catch { s = null }
      if (!live) return
      if (s && !s.error && typeof s.active === 'boolean') setState({ warning: s.active && s.warning ? s.warning : '', unknown: false })
      else setState(p => ({ warning: p.warning, unknown: true }))
      timer = setTimeout(read, POLL_MS)
    }
    read()
    return () => { live = false; clearTimeout(timer) }
  }, [runExists, api])
  return state
}
