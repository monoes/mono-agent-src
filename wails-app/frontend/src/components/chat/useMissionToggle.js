import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api } from '../../services/api.js'

const REFRESH_MS = 15000

// useMissionToggle reads the chat's Mission toggle from the CLI (`coder sessions settings get`,
// its "mission" object: {on, locked, sessionId, available, reason}) and sets it through
// `settings set --mission`. The CLI decides: it refuses turning the toggle off while the chat has
// an open mission session, and the state shown is always the one it answered with. Before the
// chat exists (no conversationId) the choice is kept here and applied once, by applyPending(id),
// when the first message creates the conversation.
export function useMissionToggle(conversationId, enabled = true) {
  const { t } = useTranslation()
  const tRef = useRef(t)
  tRef.current = t
  const [state, setState] = useState(null)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const epoch = useRef(0) // a read older than a save is dropped instead of putting the old state back
  useEffect(() => {
    if (!conversationId || !enabled) { setState(null); return undefined }
    let live = true
    const read = () => {
      const began = epoch.current
      return Promise.resolve(api.getChatMissionToggle(conversationId)).then(r => { if (live && began === epoch.current) setState(r?.mission || null) })
    }
    setError('')
    read().catch(() => { if (live) setError(tRef.current('missionToggle.loadFailed')) })
    const timer = setInterval(() => { read().catch(() => {}) }, REFRESH_MS) // a mission opened by the card locks it
    return () => { live = false; clearInterval(timer) }
  }, [conversationId, enabled])

  const set = useCallback(async on => {
    if (!conversationId) { setPending(on); return }
    epoch.current++
    setBusy(true); setError('')
    try {
      const r = await api.setChatMissionToggle(conversationId, on)
      setState(r?.mission || null)
    } catch (err) {
      setError(tRef.current('missionToggle.saveFailed', { error: String(err?.message || err) }))
    } finally {
      epoch.current++
      setBusy(false)
    }
  }, [conversationId])

  const pendingRef = useRef(false)
  pendingRef.current = pending
  const applyPending = useCallback(async id => {
    if (!pendingRef.current || !id) return
    pendingRef.current = false
    setPending(false)
    try { await api.setChatMissionToggle(id, true) } catch (err) {
      setError(tRef.current('missionToggle.saveFailed', { error: String(err?.message || err) }))
    }
  }, [])

  const view = conversationId ? state : { on: pending, locked: false, available: true }
  return { state: enabled ? view : null, busy, error, set, applyPending }
}
