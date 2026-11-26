// Wires a surface's button press to the CLI (`chat ui action`) and acts on the
// answer. A press that was accepted for the next turn sends the CLI's message as
// the next user message; if a turn is running it is held (one slot, the latest
// wins) and sent when that turn ends. Rejected/stale presses show their reason
// as a toast. Every answer is followed by onChanged, which reads the journal
// turns the CLI wrote (the press and any replaced card).
import { useCallback, useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { api, notify } from '../../services/api.js'

export function useA2UIActions({ conversationId, send, streaming, onChanged }) {
  const { t } = useTranslation()
  const held = useRef(null)
  const sendRef = useRef(send)
  sendRef.current = send
  const changedRef = useRef(onChanged)
  changedRef.current = onChanged
  const streamingRef = useRef(streaming)
  streamingRef.current = streaming

  const onA2UIAction = useCallback(async (envelope) => {
    if (!conversationId) return
    let res
    try {
      res = await api.chatUIAction(conversationId, envelope)
    } catch (e) {
      notify('a2ui', e?.message || String(e), '', t('a2ui.pressFailed'))
      return
    }
    changedRef.current?.()
    if (res?.outcome === 'rejected' || res?.outcome === 'stale') {
      notify('a2ui', res.reason || t(res.outcome === 'stale' ? 'a2ui.stale' : 'a2ui.rejected'), '', t('a2ui.pressFailed'))
    }
    if (res?.outcome === 'accepted' && res.delivered === 'next_turn' && res.message) {
      if (streamingRef.current) {
        held.current = res.message
        notify('a2ui', t('a2ui.queued'), '', t('a2ui.queuedTitle'))
      } else {
        sendRef.current(res.message)
      }
    }
  }, [conversationId, t])

  useEffect(() => {
    if (streaming || !held.current) return
    const m = held.current
    held.current = null
    sendRef.current(m)
  }, [streaming])

  return { onA2UIAction }
}
