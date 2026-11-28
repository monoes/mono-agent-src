// Wires a surface's button press to the CLI (`chat ui action`) and acts on the
// answer. A press that was accepted for the next turn sends the CLI's message as
// the next user message of the chat it was made in; if a turn is running there,
// the chat is still loading or another chat is on screen, it is held (per chat,
// in order, also while the chat is closed) and sent when that chat is shown and
// idle. Rejected/stale presses show their reason
// as a toast. Every answer is followed by onChanged, which reads the journal
// turns the CLI wrote (the press and any replaced card). submitSet sends the
// questions of an AnswerSetBar together: the cards in one press, and what the
// next turn gets (theirs and the chat blocks') as one message (#1803). A
// message the chat did not take (another send was starting) is held and tried
// again.
import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, notify } from '../../services/api.js'
import { holdMessage, takeHeld } from '../../lib/answerDrafts.js'

const WATCH_MS = 1500
const RETRY_MS = 1000
// The longest next-turn message every path takes (internal/a2ui MaxNextTurnBytes).
const MAX_MESSAGE_BYTES = 16 * 1024
const bytesOf = s => new TextEncoder().encode(s).length

export function useA2UIActions({ conversationId, send, streaming, onChanged }) {
  const { t } = useTranslation()
  const convRef = useRef(conversationId)
  convRef.current = conversationId
  const sendRef = useRef(send)
  sendRef.current = send
  const changedRef = useRef(onChanged)
  changedRef.current = onChanged
  const streamingRef = useRef(streaming)
  streamingRef.current = streaming
  const [retry, setRetry] = useState(0)
  const retryTimer = useRef(null)
  const flushing = useRef(false)
  useEffect(() => () => clearTimeout(retryTimer.current), [])
  const retryLater = useCallback(() => {
    clearTimeout(retryTimer.current)
    retryTimer.current = setTimeout(() => setRetry(n => n + 1), RETRY_MS)
  }, [])

  // The CLI's answer to a press made in this chat, null when it could not be asked.
  const press = useCallback(async (envelope) => {
    let res
    // An approved send runs as long as it takes: meanwhile read the journal, so
    // the card the CLI writes before sending shows that it is in progress.
    const watch = setInterval(() => changedRef.current?.(), WATCH_MS)
    try {
      res = await api.chatUIAction(conversationId, envelope)
    } catch (e) {
      notify('a2ui', e?.message || String(e), '', t('a2ui.pressFailed'))
      return null
    } finally {
      clearInterval(watch)
    }
    changedRef.current?.()
    if (res?.outcome === 'rejected' || res?.outcome === 'stale') {
      notify('a2ui', res.reason || t(res.outcome === 'stale' ? 'a2ui.stale' : 'a2ui.rejected'), '', t('a2ui.pressFailed'))
    }
    return res
  }, [conversationId, t])

  // Sends a next-turn message of the chat it was made in, now or once that chat
  // is shown and idle. False when it was tried and not taken.
  const deliver = useCallback(async (pressedIn, message) => {
    // The chat on screen may have changed while the press ran.
    const elsewhere = convRef.current !== pressedIn
    if (elsewhere || streamingRef.current) {
      holdMessage(pressedIn, message)
      notify('a2ui', t(elsewhere ? 'a2ui.queuedElsewhere' : 'a2ui.queued'), '', t('a2ui.queuedTitle'))
      return true
    }
    const taken = await sendRef.current(message)
    if (taken === null) {
      holdMessage(pressedIn, message)
      retryLater()
      return true
    }
    return taken !== false
  }, [t, retryLater])

  const onA2UIAction = useCallback(async (envelope) => {
    if (!conversationId) return
    const res = await press(envelope)
    if (res?.outcome === 'accepted' && res.delivered === 'next_turn' && res.message) deliver(conversationId, res.message)
  }, [conversationId, press, deliver])

  // parts: {action} and {skip} of cards, {text, remember} of chat blocks.
  const submitSet = useCallback(async (parts) => {
    if (!conversationId) return
    const actions = parts.filter(p => p.action).map(p => p.action)
    const skipped = parts.filter(p => p.skip).map(p => p.skip)
    const texts = parts.filter(p => p.text)
    const messages = []
    const added = texts.map(p => p.text).join('\n\n')
    const appendBytes = bytesOf(added)
    if (!actions.length && appendBytes > MAX_MESSAGE_BYTES) {
      notify('a2ui', t('a2ui.tooLong'), '', t('a2ui.pressFailed'))
      return
    }
    if (actions.length) {
      const res = await press({ version: '1.0', actions, ...(skipped.length ? { skipped } : {}), ...(appendBytes ? { appendBytes } : {}) })
      // Refused: nothing of the set was delivered, so none of it goes on.
      if (res?.outcome !== 'accepted') return
      if (res.delivered === 'next_turn' && res.message) messages.push(res.message)
    }
    messages.push(...texts.map(p => p.text))
    if (!messages.length || await deliver(conversationId, messages.join('\n\n'))) texts.forEach(p => p.remember?.())
  }, [conversationId, press, deliver, t])

  useEffect(() => {
    if (!conversationId || streaming || flushing.current) return
    const next = takeHeld(conversationId)
    if (!next) return
    flushing.current = true
    Promise.resolve(sendRef.current(next)).then(taken => {
      if (taken !== null) return
      holdMessage(conversationId, next, { first: true })
      retryLater()
    }).finally(() => { flushing.current = false })
  }, [conversationId, streaming, retry, retryLater])

  return { onA2UIAction, submitSet }
}
