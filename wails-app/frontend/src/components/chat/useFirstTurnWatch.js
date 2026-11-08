import { useEffect } from 'react'
import { controlRoomApi } from '../../services/controlRoom.js'

// After an open-ended start the boss's first turn is only queued: the host decides later and can refuse it (#1521: binding was
// switched off in between). This asks `doomsday status` until the turn started, failed, or was never queued, and says so once.
export const FIRST_TURN_FIRST_MS = 1500
export const FIRST_TURN_POLL_MS = 3000
export const FIRST_TURN_POLLS = 40 // about two minutes; then the person has the chat itself to look at

// Two open overlays each run a watch; the status read behind them is shared per api for a second, not one each.
const SHARE_MS = 1000
const shared = new WeakMap()
function sharedStatus(api) {
  const hit = shared.get(api)
  if (hit && Date.now() - hit.at < SHARE_MS) return hit.p
  const p = Promise.resolve(api.status())
  shared.set(api, { at: Date.now(), p })
  return p
}

// watching: true while the answer is awaited. onFailed(reason) is called once for a turn that failed or was never queued;
// onSettled() once for one that started, a run that is gone, or the end of the polls.
export function useFirstTurnWatch(watching, onFailed, onSettled, api = controlRoomApi) {
  useEffect(() => {
    if (!watching) return undefined
    let cancelled = false
    let timer = null
    let tries = 0
    const poll = async () => {
      const s = await sharedStatus(api) // never rejects
      if (cancelled) return
      if (s?.first_turn === 'failed' || s?.first_turn === 'none') { onFailed(String(s.first_turn_error || '')); return }
      if (s?.first_turn === 'started' || (s && !s.error && s.active === false) || ++tries >= FIRST_TURN_POLLS) { onSettled(); return }
      timer = setTimeout(poll, FIRST_TURN_POLL_MS)
    }
    timer = setTimeout(poll, FIRST_TURN_FIRST_MS)
    return () => { cancelled = true; clearTimeout(timer) }
  }, [watching]) // eslint-disable-line react-hooks/exhaustive-deps -- the callbacks are the caller's state setters
}
