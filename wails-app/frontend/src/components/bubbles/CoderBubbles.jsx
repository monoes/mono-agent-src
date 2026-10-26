import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, notify } from '../../services/api.js'
import { confirm } from '../ConfirmDialog.jsx'
import { BubbleDock } from './BubbleDock.jsx'
import { CoderChatOverlay } from './CoderChatOverlay.jsx'
import { OrgBubbleOverlay } from './OrgBubbleOverlay.jsx'
import { QueuedCoderStack, useQueuedCoderChats } from './QueuedCoderBubble.jsx'
import { isOrgBubble, profileFolderName } from '../../lib/coderBubbles.js'
import { folderName } from '../chat/useCoderMode.js'

// useProfiles lists the profiles, to name a chat that works in a profile's
// own folder by the profile (#477); asked again when a chat in another
// folder opens. A list that cannot be read leaves folder names.
function useProfiles(bubbles) {
  const folders = bubbles.map(b => b.cwd).filter(Boolean).sort().join('\n')
  const [profiles, setProfiles] = useState([])
  useEffect(() => {
    if (!folders) return
    let current = true
    Promise.resolve().then(() => api.getProfiles())
      .then(list => { if (current) setProfiles(Array.isArray(list) ? list : []) })
      .catch(() => {})
    return () => { current = false }
  }, [folders])
  return profiles
}

// CoderBubbles puts the bubble dock and the expanded coder chat on screen
// (#227). store is useCoderBubbles(), held by App so the assistant panel
// can open coder chats into it. Only the expanded chat is mounted as a
// full chat; collapsed ones live as summaries in the store.
// onCloseOrg(org, needs) hears that the person closed an org bubble while
// needs items waited, so it isn't reopened for the same ones.
export default function CoderBubbles({ store, onNavigate, onCloseOrg, onOpenArtifact }) {
  const { t } = useTranslation()
  const profiles = useProfiles(store.bubbles)
  const titleOf = useCallback(cwd => profileFolderName(cwd, profiles) || folderName(cwd), [profiles])
  const bubbleEls = useRef({})
  const registerEl = useCallback((key, el) => {
    if (el) bubbleEls.current[key] = el
    else delete bubbleEls.current[key]
  }, [])

  const collapseRef = useRef(() => {})
  const open = useCallback((key) => {
    if (store.expandedKey === key) collapseRef.current()
    else store.expand(key)
  }, [store])

  // Closing a chat whose turn is running asks first, and then stops the
  // turn: a closed bubble has nowhere left to show it. Collapsing keeps a
  // chat running instead.
  // A new chat with an unsent draft asks too: closing it loses the text.
  // Closing an org bubble only closes the bubble: the org keeps running.
  const closeChat = useCallback(async (key) => {
    const summary = store.summaryOf(key)
    const bubble = store.bubbles.find(b => b.key === key)
    if (isOrgBubble(bubble)) { onCloseOrg?.(bubble.orgName, summary.needs || 0); store.close(key); return }
    if (bubble && !bubble.conversationId && String(store.getView(key).draft || '').trim()) {
      const ok = await confirm(t('bubbles.confirmDiscardBody'), {
        title: t('bubbles.confirmDiscardTitle'), confirmLabel: t('bubbles.discard'), cancelLabel: t('bubbles.keepOpen'), danger: true,
      })
      if (!ok) return
    } else if (summary.activeTurnId && bubble?.conversationId) {
      const ok = await confirm(t('bubbles.confirmCloseBody'), {
        title: t('bubbles.confirmCloseTitle'), confirmLabel: t('bubbles.stopAndClose'), cancelLabel: t('bubbles.keepOpen'), danger: true,
      })
      if (!ok) return
      try {
        await api.stopChatTurn(bubble.conversationId, summary.activeTurnId)
      } catch (err) {
        notify('chat', t('bubbles.couldNotStop', { error: String(err?.message || err) }))
      }
    }
    store.close(key)
  }, [store, t, onCloseOrg])

  // Collapsing a new chat that never got a message or any typed text
  // discards it, so trying Coder and changing your mind leaves no bubble.
  const collapse = useCallback(() => {
    const b = store.bubbles.find(x => x.key === store.expandedKey)
    if (b && !isOrgBubble(b) && !b.conversationId && !String(store.getView(b.key).draft || '').trim()) store.close(b.key)
    else store.collapse()
  }, [store])

  // Queued coder chats (C5): Start creates the conversation in the CLI,
  // runs its first turn with the page context attached, and opens it as a
  // bubble. A turn that won't start leaves its prompt as the bubble's draft.
  const queue = useQueuedCoderChats()
  const startQueued = useCallback(async (item, cwd) => {
    const res = await api.coderQueueStart(item.id, cwd || '')
    queue.drop(item.id)
    let failure = ''
    try {
      const started = await api.startChatTurn(res.conversationId, res.turnId, res.prompt, false, false, res.attachments || [])
      if (started?.ok === false) failure = t('bubbles.couldNotStart', { status: started.status })
    } catch (err) {
      failure = String(err?.message || err)
    }
    const key = store.openConversation({ id: res.conversationId, cwd: res.cwd, model: res.model, runtime: res.runtime })
    if (failure) {
      store.setView(key, { draft: res.prompt })
      notify('chat', failure)
    }
  }, [store, queue, t])
  const dismissQueued = useCallback(async (item) => {
    await api.coderQueueDismiss(item.id)
    queue.drop(item.id)
  }, [queue])

  collapseRef.current = collapse
  const expanded = store.bubbles.find(b => b.key === store.expandedKey)
  const originRect = expanded ? bubbleEls.current[expanded.key]?.getBoundingClientRect() : null

  return (
    <>
      {expanded && isOrgBubble(expanded) && (
        <OrgBubbleOverlay
          key={expanded.slot || expanded.key}
          bubble={expanded}
          store={store}
          originRect={originRect}
          onCollapse={collapse}
          onClose={() => closeChat(expanded.key)}
          onNavigate={(page, params) => { collapse(); onNavigate?.(page, params) }}
        />
      )}
      {expanded && !isOrgBubble(expanded) && (
        <CoderChatOverlay
          key={expanded.key}
          bubble={expanded}
          store={store}
          originRect={originRect}
          onCollapse={collapse}
          onCloseChat={() => closeChat(expanded.key)}
          titleOf={titleOf}
          onOpenArtifact={onOpenArtifact}
          onNavigate={(...args) => { collapse(); onNavigate?.(...args) }}
        />
      )}
      {!expanded && <QueuedCoderStack items={queue.items} side={store.side} onStart={startQueued} onDismiss={dismissQueued} />}
      <BubbleDock store={store} onOpen={open} onClose={closeChat} registerEl={registerEl} titleOf={titleOf} />
    </>
  )
}
