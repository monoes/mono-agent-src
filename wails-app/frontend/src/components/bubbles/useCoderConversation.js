import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, notify } from '../../services/api.js'
import { useChatStream, loadTurnState } from '../chat/useChatStream.js'
import { newTurnId } from '../chat/turnIds.js'
import { composeLiveAnnouncement } from '../chat/liveAnnouncement.js'
import { getAssistantTools, getAssistantAllowRuns } from '../../lib/assistantTools.js'

// useCoderConversation is one expanded coder bubble's conversation (#227):
// it loads the turns from the journal (`chat history`), re-attaches to a
// turn that is still running (a chat keeps working while its bubble is
// collapsed, so expanding it must pick the live turn back up), and sends
// and stops turns. Turns run in the CLI; this only starts, stops and reads
// them.
//
// create() is called for the first message of a draft: it creates the
// conversation and returns it ({ id, cwd, model, access }). access is the
// open conversation's level: a scoped turn gets monoagent's tools as the
// Settings choose them, a coding one never does.
export function useCoderConversation({ conversationId, create, access = 'full' }) {
  const { t } = useTranslation()
  const [messages, setMessages] = useState([])
  const [activeTurnId, setActiveTurnId] = useState('')
  const [convId, setConvId] = useState(conversationId || '')
  const [loading, setLoading] = useState(!!conversationId)
  const [loadError, setLoadError] = useState('')
  const [stopRequested, setStopRequested] = useState(false)
  const liveTurn = useChatStream({ conversationId: convId, turnId: activeTurnId })
  const activeRef = useRef('')
  activeRef.current = activeTurnId
  // sending is set before send's first await, so a second Enter while the
  // conversation is being created (or the turn started) doesn't send twice.
  const sending = useRef(false)
  const accessRef = useRef(access)
  accessRef.current = access

  useEffect(() => { if (conversationId) setConvId(conversationId) }, [conversationId])

  // Load the transcript once per conversation (re-expanding a bubble mounts
  // this again and reloads from the journal).
  const loadedFor = useRef('')
  useEffect(() => {
    if (!conversationId || loadedFor.current === conversationId) return
    loadedFor.current = conversationId
    let cancelled = false
    setLoading(true)
    setLoadError('')
    Promise.resolve().then(() => api.getChatTurns(conversationId, '', 50)).then(async (res) => {
      const turns = (Array.isArray(res?.items) ? res.items : []).slice().reverse() // oldest first
      const built = []
      let running = ''
      for (const turn of turns) {
        if (cancelled) return
        const user = { role: 'user', content: turn.prompt }
        built.push(user)
        if (turn.status === 'active' && turn.ownedByThisInstance !== false) {
          // Still running: the live stream (useChatStream) shows it.
          running = turn.id
          continue
        }
        // eslint-disable-next-line no-await-in-loop
        const state = await loadTurnState(conversationId, turn)
        if (state?.attachments) user.attachments = state.attachments
        if (state) built.push({ role: 'turn', turnId: turn.id, state, ownedByThisInstance: turn.ownedByThisInstance !== false })
      }
      if (cancelled) return
      setMessages(built)
      setActiveTurnId(running)
    }).catch(err => {
      if (!cancelled) setLoadError(String(err?.message || err))
    }).finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true; loadedFor.current = '' }
  }, [conversationId])

  // A finished live turn joins the transcript, and is announced once through
  // the view's persistent live region (the per-turn status region unmounts at
  // that moment). A notice that lands mid-turn is announced as it arrives, and
  // not again in the finish summary. Both live in one effect: the terminal
  // event and a notice can land in the same render.
  const [announcement, setAnnouncement] = useState('')
  const announcedNotices = useRef(0)
  useEffect(() => {
    if (!activeTurnId) return
    if (liveTurn.terminal) {
      setMessages(msgs => [...msgs, { role: 'turn', turnId: activeTurnId, state: liveTurn }])
      setAnnouncement(composeLiveAnnouncement(liveTurn, announcedNotices.current))
      announcedNotices.current = 0
      setActiveTurnId('')
      setStopRequested(false)
      return
    }
    const notices = liveTurn.notices || []
    if (notices.length > announcedNotices.current) {
      setAnnouncement(notices.slice(announcedNotices.current).map(n => n.message).join(' '))
      announcedNotices.current = notices.length
    }
  }, [activeTurnId, liveTurn])

  // send resolves true once the turn started, false when it failed (the
  // error is in the transcript), and null when there was nothing to send or
  // a turn is already starting or running. attachments are the composer's
  // (chatAttachments.js readAttachment): each is saved to the chat spool and
  // the turn gets their paths.
  const send = useCallback(async (text, attachments = []) => {
    const prompt = String(text || '').trim()
    if (!prompt || activeRef.current || sending.current) return null
    sending.current = true
    const shown = attachments.map(({ name, mime, size, dataUrl }) => ({ name, mime, size, dataUrl }))
    setMessages(msgs => [...msgs, shown.length ? { role: 'user', content: prompt, attachments: shown } : { role: 'user', content: prompt }])
    setStopRequested(false)
    try {
      let id = convId
      if (!id) {
        const conv = await create()
        id = conv.id
        if (conv.access) accessRef.current = conv.access
        loadedFor.current = id // nothing to load: this chat starts here
        setConvId(id)
        if (conv.workspace) setMessages(msgs => [...msgs.slice(0, -1), { role: 'coder-init', workspace: conv.workspace }, ...msgs.slice(-1)])
      }
      const turnId = newTurnId()
      const paths = []
      for (const a of attachments) {
        // eslint-disable-next-line no-await-in-loop
        paths.push(await api.saveChatAttachment(id, a.name, a.base64))
      }
      // Coder turns never get monoagent tools: the CLI refuses --tools for
      // a coder conversation. A scoped one gets what Settings allow.
      const scoped = accessRef.current === 'scoped'
      const tools = scoped && getAssistantTools()
      const allowRuns = scoped && getAssistantAllowRuns()
      const res = await (paths.length
        ? api.startChatTurn(id, turnId, prompt, tools, allowRuns, paths)
        : api.startChatTurn(id, turnId, prompt, tools, allowRuns))
      if (res?.ok === false) {
        setMessages(msgs => [...msgs, { role: 'error', content: t('bubbles.couldNotStart', { status: res.status }) }])
        return false
      }
      activeRef.current = turnId
      announcedNotices.current = 0
      setActiveTurnId(turnId)
      return true
    } catch (err) {
      setMessages(msgs => [...msgs, { role: 'error', content: String(err?.message || err), code: err?.code || '' }])
      return false
    } finally {
      sending.current = false
    }
  }, [convId, create, t])

  const stop = useCallback(async () => {
    if (!convId || !activeRef.current) return
    setStopRequested(true)
    try {
      await api.stopChatTurn(convId, activeRef.current)
    } catch (err) {
      setStopRequested(false)
      notify('chat', t('bubbles.couldNotStop', { error: String(err?.message || err) }))
    }
  }, [convId, t])

  // reset empties the chat so the next send creates a new conversation.
  // Blocked while a turn runs.
  const reset = useCallback(() => {
    if (activeRef.current) return false
    loadedFor.current = ''
    setMessages([])
    setConvId('')
    setLoadError('')
    return true
  }, [])

  return { reset, messages, announcement, liveTurn, activeTurnId, streaming: !!activeTurnId, stopRequested, loading, loadError, send, stop, conversationId: convId }
}
