import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api } from '../../services/api.js'
import { openStartApi } from '../../lib/openStartApi.js'

const REFRESH_MS = 15000

// useChatMode reads the chat's mode from the CLI (`coder sessions settings get`, its "chatMode" object:
// {mode, locked, reason, options:[{mode, available, reason}]}) and changes it with `settings set --chat-mode`. The
// CLI decides everything: which modes can be chosen (the reasons of the disabled ones come with the options), that
// the mode is locked from the first message on, and what a refusal says. Before the chat exists the options come
// from `coder sessions chat-modes` for the access level and the choice is only kept here (pendingMode): it goes to
// the CLI together with the conversation (chatAccessApi.create), so there is no write of its own and no first message
// that can run before it.
// Mission and open-ended chats need Dynamic org; the CLI switches it on when the mode is chosen, and the hook says so
// (notice) and tells onDynamicOrg so the pane shows the org mode as on.
export function useChatMode(conversationId, enabled = true, access = '', onDynamicOrg = null) {
  const { t } = useTranslation()
  const tRef = useRef(t)
  tRef.current = t
  const [view, setView] = useState(null)
  const [draftOptions, setDraftOptions] = useState(null)
  const [pending, setPending] = useState('off')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [busy, setBusy] = useState(false)
  const [checking, setChecking] = useState(false)
  const epoch = useRef(0) // a read older than a save is dropped instead of putting the old state back
  const read = useCallback(() => {
    const began = epoch.current
    return Promise.resolve(api.getChatMissionToggle(conversationId)).then(r => { if (began === epoch.current) setView(r?.chatMode || null) })
  }, [conversationId])

  useEffect(() => {
    if (!conversationId || !enabled) { setView(null); return undefined }
    let live = true
    setError('')
    read().catch(() => { if (live) setError(tRef.current('chatMode.loadFailed')) })
    const timer = setInterval(() => { read().catch(() => {}) }, REFRESH_MS) // the first message locks it
    return () => { live = false; clearInterval(timer) }
  }, [conversationId, enabled, read])

  useEffect(() => {
    if (conversationId || !enabled || !access) { setDraftOptions(null); return undefined }
    let live = true
    Promise.resolve().then(() => api.getChatModeOptions(access)).then(r => { if (live) setDraftOptions(Array.isArray(r?.options) ? r.options : null) }).catch(() => { if (live) setDraftOptions(null) })
    return () => { live = false }
  }, [conversationId, enabled, access])

  useEffect(() => { if (conversationId) setPending('off') }, [conversationId])

  const onDynamicOrgRef = useRef(onDynamicOrg)
  onDynamicOrgRef.current = onDynamicOrg
  const pendingRef = useRef('off')
  pendingRef.current = pending

  const set = useCallback(async mode => {
    setNotice('')
    if (!conversationId) { setPending(mode); if (mode !== 'off') setNotice(tRef.current('chatMode.orgSwitched')); return }
    epoch.current++
    setBusy(true); setError('')
    try {
      const r = await api.setChatMode(conversationId, mode)
      setView(r?.chatMode || null)
      if (mode !== 'off') { setNotice(tRef.current('chatMode.orgSwitched')); onDynamicOrgRef.current?.() }
    } catch (err) {
      setError(tRef.current('chatMode.saveFailed', { error: String(err?.message || err) }))
    } finally {
      epoch.current++
      setBusy(false)
    }
  }, [conversationId])

  // The open mode is offered "pending": the real probe (a transient unit) runs only here, on the person's own action, and
  // the app caches it for a minute and shares a probe in flight. Nothing reads it on a timer or on mount.
  const checkOpen = useCallback(async () => {
    setChecking(true); setError('')
    try {
      const r = await openStartApi.entry()
      if (!r?.mode_available) { setError(tRef.current('chatMode.openRefused', { reason: String(r?.reason || r?.error || r?.detail || '') })); return }
      await set('open')
    } catch (err) {
      setError(tRef.current('chatMode.openRefused', { reason: String(err?.message || err) }))
    } finally {
      setChecking(false)
    }
  }, [set])

  const draft = draftOptions ? { mode: pending, locked: false, options: draftOptions } : null
  const state = enabled ? (conversationId ? view : draft) : null
  return { state, busy: busy || checking, checking, checkOpen, error, notice, set, refresh: read, pendingMode: () => (enabled && pendingRef.current !== 'off' ? pendingRef.current : '') }
}
