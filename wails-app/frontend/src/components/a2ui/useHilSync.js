// While a conversation view is open, asks the CLI to surface pending HIL items
// as cards (`chat ui sync-hil`): once on open, then every 10 s while the window
// is visible and focused. Never two calls at once; a failure only warns.
// onChanged runs when the CLI added or closed a card, to read it.
import { useEffect, useRef } from 'react'
import { api } from '../../services/api.js'

export const HIL_SYNC_ENABLED = true
export const HIL_SYNC_MS = 10000

export function useHilSync(conversationId, onChanged, enabled = HIL_SYNC_ENABLED) {
  const changedRef = useRef(onChanged)
  changedRef.current = onChanged
  useEffect(() => {
    if (!enabled || !conversationId) return undefined
    let inflight = false
    let focused = true
    let stopped = false
    const run = async () => {
      if (inflight || stopped || document.hidden || !focused) return
      inflight = true
      try {
        const r = await api.chatUISyncHIL(conversationId)
        if (!stopped && (r?.surfaced?.length || r?.resolved?.length)) await changedRef.current?.()
      } catch (e) { console.warn('a2ui: sync-hil failed', e) } finally { inflight = false }
    }
    const onFocus = () => { focused = true }
    const onBlur = () => { focused = false }
    window.addEventListener('focus', onFocus)
    window.addEventListener('blur', onBlur)
    run()
    const timer = setInterval(run, HIL_SYNC_MS)
    return () => {
      stopped = true
      clearInterval(timer)
      window.removeEventListener('focus', onFocus)
      window.removeEventListener('blur', onBlur)
    }
  }, [conversationId, enabled])
}
