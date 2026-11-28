// Wires a surface's button press to the CLI (`chat ui action`) and acts on the
// answer. A press that was accepted for the next turn sends the CLI's message as
// the next user message of the chat it was made in; if a turn is running there,
// or another chat is on screen, it is held (one slot per chat, the latest wins)
// and sent when that chat is shown and idle. Rejected/stale presses show their reason
// as a toast. Every answer is followed by onChanged, which reads the journal
// turns the CLI wrote (the press and any replaced card).
import { useCallback, useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { api, notify } from '../../services/api.js'

const WATCH_MS = 1500

export function useA2UIActions({ conversationId, send, streaming, onChanged }) {
  const { t } = useTranslation()
  const held = useRef(new Map()) // conversation id -> message waiting to be sent there
  const convRef = useRef(conversationId)
  convRef.current = conversationId
  const sendRef = useRef(send)
  sendRef.current = send
  const changedRef = useRef(onChanged)
  changedRef.current = onChanged
  const streamingRef = useRef(streaming)
  streamingRef.current = streaming

  const onA2UIAction = useCallback(async (envelope) => {
    if (!conversationId) return
    const pressedIn = conversationId
    let res
    // An approved send runs as long as it takes: meanwhile read the journal, so
    // the card the CLI writes before sending shows that it is in progress.
    const watch = setInterval(() => changedRef.current?.(), WATCH_MS)
    try {
      res = await api.chatUIAction(conversationId, envelope)
    } catch (e) {
      notify('a2ui', e?.message || String(e), '', t('a2ui.pressFailed'))
      return
    } finally {
      clearInterval(watch)
    }
    changedRef.current?.()
    if (res?.outcome === 'rejected' || res?.outcome === 'stale') {
      notify('a2ui', res.reason || t(res.outcome === 'stale' ? 'a2ui.stale' : 'a2ui.rejected'), '', t('a2ui.pressFailed'))
    }
    if (res?.outcome === 'accepted' && res.delivered === 'next_turn' && res.message) {
      // The chat on screen may have changed while the press ran.
      const elsewhere = convRef.current !== pressedIn
      if (elsewhere || streamingRef.current) {
        held.current.set(pressedIn, res.message)
        notify('a2ui', t(elsewhere ? 'a2ui.queuedElsewhere' : 'a2ui.queued'), '', t('a2ui.queuedTitle'))
      } else {
        sendRef.current(res.message)
      }
    }
  }, [conversationId, t])

  useEffect(() => {
    if (streaming || !held.current.has(conversationId)) return
    const m = held.current.get(conversationId)
    held.current.delete(conversationId)
    sendRef.current(m)
  }, [conversationId, streaming])

  return { onA2UIAction }
}
