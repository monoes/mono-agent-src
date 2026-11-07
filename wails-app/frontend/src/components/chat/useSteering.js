import { useCallback, useRef, useState } from 'react'
import { api } from '../../services/api.js'

// useSteering sends the person's text to the boss behind a chat whose open-ended run is going. The CLI wraps it as
// untrusted guidance and refuses when no boss stands behind the chat; nothing here is an answer to a question or an
// approval. send resolves true once the text was queued, false when it was refused (error holds the CLI's reason).
export function useSteering(conversationId) {
  const [last, setLast] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const inFlight = useRef(false)
  const send = useCallback(async (text) => {
    const body = String(text || '').trim()
    if (!body || !conversationId || inFlight.current) return false
    inFlight.current = true
    setBusy(true)
    setError('')
    try {
      const res = await api.steerBoss(conversationId, body)
      if (res?.error) { setError(String(res.error)); return false }
      setLast(body)
      return true
    } catch (err) {
      setError(String(err?.message || err))
      return false
    } finally {
      inFlight.current = false
      setBusy(false)
    }
  }, [conversationId])
  return { send, last, error, busy }
}
