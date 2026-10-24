import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Minimize2, X, ChevronUp, ChevronDown } from 'lucide-react'
import { api, notify } from '../../services/api.js'
import { CoderHeader, CoderBadge } from '../chat/CoderHeader.jsx'
import { folderName } from '../chat/useCoderMode.js'
import { runtimeLabel } from '../../lib/runtimeLabels.js'
import { CoderChatView } from './CoderChatView.jsx'
import { PaneSettings, SettingsButton } from './PaneSettings.jsx'
import { useVerifiedAccess } from './useChatPaneData.js'
import { chatAccessApi } from '../../lib/chatAccessApi.js'
import { isAccess, isCoderAccess, isConfinedTask } from '../../lib/chatAccess.js'
import { useCoderConversation } from './useCoderConversation.js'
import { TaskHeaderCard } from './TaskHeaderCard.jsx'
import { shouldCollapseOnBackdrop } from '../../lib/coderBubbles.js'
import { LEAD_ID } from '../../lib/orgStage.js'
import { OrgStage } from '../stage/OrgStage.jsx'
import { StageDrawer } from '../stage/StageDrawer.jsx'
import './bubbles.css'
import { isModalOpenAbove } from './modalAbove.js'

const mono = 'var(--font-mono)'
const chip = { fontFamily: mono, fontSize: 9.5, color: 'var(--text-muted)', border: '1px solid var(--border)', borderRadius: 999, padding: '1px 7px', whiteSpace: 'nowrap' }
const CLOSE_MS = 170

// isModalOpen reports whether a dialog is open on top of the overlay
// (ConfirmDialog renders role="dialog" aria-modal="true").
function isModalOpen() {
  return isModalOpenAbove()
}

// shownTurn is the turn the stage shows: the running one, else the last
// finished one.
function shownTurn(conv) {
  if (conv.streaming) return { state: conv.liveTurn, turnId: conv.activeTurnId, isLive: true }
  for (let i = conv.messages.length - 1; i >= 0; i--) {
    const m = conv.messages[i]
    if (m.role === 'turn') return { state: m.state, turnId: m.turnId, isLive: false }
  }
  return null
}

// chatAgentOf is the agent whose rows the chat keeps when a node is
// selected: a native subagent's rows live under the worker that called it.
function chatAgentOf(stage, id) {
  let cur = stage?.nodes?.[id]
  while (cur && cur.native && cur.parentId) cur = stage.nodes[cur.parentId]
  return cur ? cur.id : id
}

function prefersReducedMotion() {
  return typeof window !== 'undefined' && !!window.matchMedia?.('(prefers-reduced-motion: reduce)').matches
}

// CoderChatOverlay is an expanded coder bubble (#227): the org stage on
// top, the chat below, over a dimmed backdrop. A click on the backdrop,
// Esc, or the collapse button shrinks it back into its bubble; none of
// them stop a running turn; the × closes the chat (onCloseChat, which
// asks first when a turn is running). originRect is the bubble's screen rectangle, so
// the overlay grows out of it.
export function CoderChatOverlay({ bubble, store, originRect, onCollapse, onCloseChat, onNavigate, onOpenArtifact, titleOf = folderName }) {
  const { t } = useTranslation()
  const view = store.getView(bubble.key)
  const [closing, setClosing] = useState(false)
  const [narrow, setNarrow] = useState(() => typeof window !== 'undefined' && window.innerWidth < 640)
  const [stageRatio, setStageRatio] = useState(view.stageRatio ?? 0.35)
  const [stageCollapsed, setStageCollapsed] = useState(!!view.stageCollapsed)
  const [draft, setDraft] = useState(view.draft || '')
  const [setup, setSetup] = useState(view.setup || { workspace: { kind: 'root' }, runtime: '', model: '', effort: '' })
  const [selectedAgent, setSelectedAgent] = useState(null)
  const selectedRef = useRef(null)
  const bodyRef = useRef(null)
  const pressOnBackdrop = useRef(false)

  // create makes the conversation for a draft's first message, at the access
  // level, runtime, model and effort picked in its setup (ChatSetup). Above
  // scoped it works in a folder: the one picked, else the profile's default.
  const create = useCallback(async () => {
    const runtime = setup.runtime
    const access = isAccess(setup.access) ? setup.access : 'scoped'
    let cwd = ''
    let workspace = null
    if (isCoderAccess(access)) {
      cwd = setup.workspace?.kind === 'folder' ? setup.workspace.path : ''
      if (!cwd) {
        workspace = await api.coderWorkspaceDefault(runtime)
        cwd = workspace.path
      }
    }
    const made = await chatAccessApi.create('general', runtime, setup.model, setup.effort || '', access, cwd)
    const bound = {
      id: made.id, cwd: made.cwd || cwd, model: made.model || setup.model, runtime: made.runtimeId || runtime,
      access: isAccess(made.access) ? made.access : access, verified: isAccess(made.access), effort: made.effort || setup.effort || '', orgMode: made.orgMode || '',
    }
    store.bindConversation(bubble.key, bound)
    return { ...bound, workspace }
  }, [setup, store, bubble.key])
  // A conversation's level counts once the backend has said it; a cached one is a hint only.
  const verify = useVerifiedAccess(bubble, store)
  const access = bubble.conversationId ? (bubble.verified ? bubble.access : '') : (setup.access || '')
  const confined = isConfinedTask({ kind: bubble.taskKind || view.task?.kind })
  const [settingsOpen, setSettingsOpen] = useState(false)
  const settingsBtn = useRef(null)
  const settingsOpenRef = useRef(false)
  settingsOpenRef.current = settingsOpen
  const closeSettings = useCallback(() => { setSettingsOpen(false); settingsBtn.current?.focus?.() }, [])
  const conv = useCoderConversation({ conversationId: bubble.conversationId, create, access })

  // The title is the backend's redacted one, read again after each turn; a raw
  // prompt never becomes a title.
  const wasStreaming = useRef(false)
  useEffect(() => {
    if (wasStreaming.current && !conv.streaming && bubble.conversationId) verify.refresh()
    wasStreaming.current = conv.streaming
    // refresh is stable per conversation.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [conv.streaming])
  useEffect(() => { store.setView(bubble.key, { draft }) }, [store, bubble.key, draft])
  useEffect(() => { store.setView(bubble.key, { setup }) }, [store, bubble.key, setup])
  useEffect(() => { store.setView(bubble.key, { stageRatio, stageCollapsed }) }, [store, bubble.key, stageRatio, stageCollapsed])

  useEffect(() => {
    const onResize = () => setNarrow(window.innerWidth < 640)
    window.addEventListener('resize', onResize)
    return () => window.removeEventListener('resize', onResize)
  }, [])

  // collapse plays the shrink animation, then hands back to the bubble.
  const collapse = useCallback(() => {
    if (closing) return
    if (prefersReducedMotion()) { onCollapse(); return }
    setClosing(true)
    setTimeout(onCollapse, CLOSE_MS)
  }, [closing, onCollapse])

  // Esc closes the settings popover, then an open node drawer, then collapses.
  // It never stops a running turn: a collapsed chat keeps working, and Stop is
  // its own button. Captured on window first, so the assistant panel under the
  // overlay (which also closes on Esc) doesn't react to the same key; an open
  // dialog handles its own Esc.
  useEffect(() => {
    const onKey = (e) => {
      if (e.key !== 'Escape' || isModalOpen()) return
      // The History drawer handles its own Escape when focus is inside it, so
      // this overlay stays open (and the key keeps travelling to the drawer).
      if (e.target?.closest?.('[data-testid="history-panel"]')) return
      e.stopImmediatePropagation()
      if (settingsOpenRef.current) { closeSettings(); return }
      if (selectedRef.current) { setSelectedAgent(null); return }
      collapse()
    }
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
  }, [collapse, closeSettings])

  // Grow out of the bubble: the transform origin is the bubble's center,
  // relative to the overlay's own box.
  const panelRef = useRef(null)
  useLayoutEffect(() => {
    const el = panelRef.current
    if (!el || !originRect) return
    const box = el.getBoundingClientRect()
    el.style.setProperty('--origin-x', `${originRect.left + originRect.width / 2 - box.left}px`)
    el.style.setProperty('--origin-y', `${originRect.top + originRect.height / 2 - box.top}px`)
  }, [originRect])

  // Focus the composer on open; give focus back on close.
  useEffect(() => {
    const prev = document.activeElement
    const ta = panelRef.current?.querySelector('textarea:not([disabled])')
    ta?.focus?.()
    return () => prev?.focus?.()
  }, [])

  const startDrag = (e) => {
    e.preventDefault()
    const body = bodyRef.current?.getBoundingClientRect()
    if (!body) return
    const onMove = (ev) => setStageRatio(Math.min(0.7, Math.max(0.15, (ev.clientY - body.top) / body.height)))
    const onUp = () => { window.removeEventListener('pointermove', onMove); window.removeEventListener('pointerup', onUp) }
    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
  }
  const onDividerKey = (e) => {
    if (e.key === 'ArrowUp') { e.preventDefault(); setStageRatio(r => Math.max(0.15, r - 0.05)) }
    if (e.key === 'ArrowDown') { e.preventDefault(); setStageRatio(r => Math.min(0.7, r + 0.05)) }
  }

  const shown = shownTurn(conv)
  const stage = shown?.state?.stage || null
  // Worker ids repeat per turn (w1, w2, …): a selection belongs to the turn
  // the stage shows, and a new turn clears it.
  const shownTurnId = shown?.turnId || ''
  useEffect(() => { setSelectedAgent(null) }, [shownTurnId])
  const selectedNode = selectedAgent ? (stage?.nodes?.[selectedAgent] || null) : null
  selectedRef.current = selectedNode ? selectedAgent : null
  const selectAgent = useCallback(id => setSelectedAgent(cur => (cur === id ? null : id)), [])
  const agentFilter = selectedNode ? chatAgentOf(stage, selectedNode.id) : null
  const filterName = selectedNode ? (selectedNode.id === LEAD_ID ? t('bubbles.lead') : stage.nodes[agentFilter]?.role || agentFilter) : ''
  // stopAgent stops one node of the running turn (#255): a worker alone,
  // or the lead, which is the whole turn. The stage then updates from the
  // journal's agent events. A finished turn has nothing to stop.
  const liveConvId = conv.conversationId
  const liveTurnId = shown?.isLive ? shownTurnId : ''
  const stopAgent = useCallback(async (id) => {
    try {
      if (id === LEAD_ID) await api.stopChatTurn(liveConvId, liveTurnId)
      else await api.stopChatAgent(liveConvId, liveTurnId, id)
    } catch (err) {
      notify('chat', t('bubbles.couldNotStop', { error: String(err?.message || err) }))
    }
  }, [liveConvId, liveTurnId, t])

  const title = bubble.title || (bubble.cwd ? titleOf(bubble.cwd) : t('bubbles.newChat'))
  const showStage = access !== 'scoped' && !confined
  const runtime = bubble.conversationId ? bubble.runtime : setup.runtime
  const model = bubble.model || setup.model

  return (
    <>
      <div
        className={`bubble-backdrop${closing ? ' closing' : ''}`}
        data-testid="bubble-backdrop"
        onPointerDown={e => { pressOnBackdrop.current = e.target === e.currentTarget }}
        onClick={e => {
          const collapseIt = e.target === e.currentTarget && shouldCollapseOnBackdrop({
            pressStartedOnBackdrop: pressOnBackdrop.current,
            selectionText: window.getSelection?.()?.toString() || '',
            modalOpen: isModalOpen(),
          })
          pressOnBackdrop.current = false
          if (collapseIt) collapse()
        }}
      />
      <div ref={panelRef} role="dialog" aria-label={t('bubbles.chatLabel', { name: title })} data-testid="bubble-overlay"
        className={`bubble-overlay${closing ? ' closing' : ''}${narrow ? ' narrow' : ''}`}>
        <div style={{ position: 'relative', display: 'flex', alignItems: 'center', gap: 8, padding: '9px 12px', borderBottom: '1px solid var(--border)', flexShrink: 0 }}>
          {/* Title and chips wrap among themselves on a narrow pane; the buttons stay on the right. */}
          <div style={{ flex: '1 1 0', minWidth: 0, display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
            {/* The folder row below carries the badge once there is a folder. */}
            {!bubble.cwd && isCoderAccess(access) && <CoderBadge />}
            <span style={{ fontFamily: 'var(--font-display)', fontSize: 13, fontWeight: 600, color: 'var(--text)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', maxWidth: '100%' }}>{title}</span>
            {(runtime || model) && (
              <span style={{ ...chip, color: 'var(--cyan)', borderColor: 'var(--border-bright)', overflow: 'hidden', textOverflow: 'ellipsis', maxWidth: '100%' }}>
                {[runtime && runtimeLabel(runtime), model].filter(Boolean).join(' · ')}
              </span>
            )}
            {confined ? (
              <span data-testid="chip-confined" title={t('bubbles.pane.confinedWhy')} style={chip}>{t('bubbles.pane.confinedChip')}</span>
            ) : isAccess(access) ? (
              <span data-testid="chip-access" title={t(`bubbles.pane.allows.${access}`)} style={chip}>{t('bubbles.pane.accessChip', { level: t(`bubbles.pane.level.${access}`) })}</span>
            ) : bubble.conversationId && (
              <span data-testid="chip-access" title={t('bubbles.pane.accessUnknownWhy')} style={chip}>{t('bubbles.pane.accessChip', { level: t('bubbles.pane.accessUnknown') })}</span>
            )}
          </div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 4, flexShrink: 0 }}>
            {showStage && (
              <button type="button" className="btn btn-ghost btn-sm" onClick={() => setStageCollapsed(c => !c)}
                aria-expanded={!stageCollapsed} title={stageCollapsed ? t('bubbles.showOrg') : t('bubbles.hideOrg')} style={{ gap: 4, fontSize: 10 }}>
                {stageCollapsed ? <ChevronDown size={12} /> : <ChevronUp size={12} />} {t('bubbles.org')}
              </button>
            )}
            {bubble.conversationId && <SettingsButton buttonRef={settingsBtn} open={settingsOpen} onToggle={() => setSettingsOpen(o => !o)} />}
            <button type="button" className="btn btn-ghost btn-sm" onClick={collapse} title={t('bubbles.collapse')} aria-label={t('bubbles.collapse')} style={{ padding: '3px 6px' }}>
              <Minimize2 size={13} />
            </button>
            <button type="button" className="btn btn-ghost btn-sm" onClick={onCloseChat} title={t('bubbles.closeChat')} aria-label={t('bubbles.closeChat')} style={{ padding: '3px 6px' }}>
              <X size={13} />
            </button>
          </div>
          {settingsOpen && bubble.conversationId && (
            <PaneSettings bubble={bubble} access={access} store={store} confined={confined} onNavigate={page => { collapse(); onNavigate?.(page) }} onClose={closeSettings} />
          )}
        </div>
        {bubble.cwd && <CoderHeader cwd={bubble.cwd} />}
        {view.task && <TaskHeaderCard key={view.task.taskId} task={view.task} onNavigate={onNavigate} />}
        <div ref={bodyRef} style={{ flex: 1, minHeight: 0, display: 'flex', flexDirection: 'column', position: 'relative' }}>
          {showStage && !stageCollapsed && (
            <>
              <div style={{ height: `${Math.round(stageRatio * 100)}%`, flexShrink: 0 }}>
                <OrgStage stage={stage} turnId={shown?.turnId || ''} working={conv.streaming}
                  leadInfo={{ runtime, model, effort: bubble.effort || setup.effort }}
                  leadIdle={bubble.conversationId ? t('bubbles.idle') : t('bubbles.ready')}
                  selectedId={selectedAgent} onSelect={selectAgent} />
              </div>
              <div className="bubble-divider" role="separator" aria-orientation="horizontal" tabIndex={0}
                aria-label={t('bubbles.resizeOrg')} aria-valuenow={Math.round(stageRatio * 100)} aria-valuemin={15} aria-valuemax={70}
                onPointerDown={startDrag} onKeyDown={onDividerKey} />
            </>
          )}
          <CoderChatView
            conv={conv}
            isDraft={!bubble.conversationId}
            access={access}
            accessState={verify.state}
            onRetryAccess={verify.retry}
            setup={setup}
            onSetupChange={patch => setSetup(s => ({ ...s, ...patch }))}
            draft={draft}
            onDraftChange={setDraft}
            initialScrollTop={view.scrollTop}
            onScroll={top => store.setView(bubble.key, { scrollTop: top })}
            onNavigate={onNavigate}
            onOpenArtifact={onOpenArtifact}
            runtime={runtime}
            agentFilter={agentFilter}
            agentFilterTurnId={shownTurnId}
            agentFilterName={filterName}
            onClearAgentFilter={() => setSelectedAgent(null)}
          />
          {selectedNode && (
            <StageDrawer node={selectedNode} calls={shown?.state?.agentCalls} leadInfo={{ runtime, model, effort: bubble.effort || setup.effort }}
              turnId={shown?.turnId || ''} isLive={!!shown?.isLive} onClose={() => setSelectedAgent(null)}
              onStop={liveConvId && liveTurnId ? stopAgent : undefined} onNavigate={onNavigate} />
          )}
        </div>
      </div>
    </>
  )
}
