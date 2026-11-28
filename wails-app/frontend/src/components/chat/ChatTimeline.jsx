import { AlertTriangle, Info, AlertCircle, Loader, FolderOpen } from 'lucide-react'
import { ChatMarkdown } from './ChatMarkdown.jsx'
import { ToolActivityCard } from './ToolActivityCard.jsx'
import { CoderBackgroundBanner } from './CoderBackgroundBanner.jsx'
import { SandboxBadge } from './SandboxBadge.jsx'
import { UnsandboxedWarning } from './ChatWarnings.jsx'
import { AgentRow } from './AgentRow.jsx'
import { ChatBlocksScope } from './blocks/context.js'
import { A2UISurface, planTree } from '../a2ui/A2UISurface.jsx'
import { isAskCard } from '../../lib/a2uiModel.js'

const NOTICE_ICON = {
  info: Info,
  warning: AlertTriangle,
  error: AlertCircle,
}
const NOTICE_COLOR = {
  info: '#00b4d8',
  warning: '#fbbf24',
  error: '#ef4444',
}

// Whether the card draws a button that sends something: only such a card waits for the person.
const hasButton = sf => planTree(sf.components || {}).order.some(id => sf.components[id]?.component === 'Button' && sf.components[id].action?.event?.name)

function NoticeBanner({ notice }) {
  const Icon = NOTICE_ICON[notice.severity] || Info
  const color = NOTICE_COLOR[notice.severity] || '#00b4d8'
  return (
    <div style={{ display: 'flex', alignItems: 'flex-start', gap: 6, padding: '6px 8px', marginTop: 6, borderRadius: 6, background: `${color}14`, border: `1px solid ${color}40` }}>
      <Icon size={11} color={color} style={{ marginTop: 1, flexShrink: 0 }} />
      <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color }}>{notice.message}</span>
    </div>
  )
}

// ChatTimeline renders one turn's ordered parts (interleaved assistant text
// and tool steps, in actual arrival order — never re-sorted or grouped by
// kind) plus any nonfatal notices. Pure presentation over chatReducer
// state; the chat bubble supplies the state, live (via useChatStream) or
// reconstructed from history (via reduceTurnEvents).
//
// turnId/isLive are passed straight through to each ToolActivityCard — see
// its own doc comment. Defaults (isLive=true) match the pre-existing
// behavior for callers that only ever render a still-streaming turn.
//
// Coder turns (#202) add two things. A call with a parentCallId was made
// inside that subagent call: it renders nested in the parent's card, not
// at the top level. And three notice codes get their own treatment:
// coder.status (startup progress) is a transient line shown only while the
// live turn has produced nothing yet, coder.workspace a compact folder
// line, and coder.background a warning banner with "Stop all".
//
// agentFilter narrows a dynamic-org turn (#228) to one agent: "lead" keeps
// the lead's own text and tools, a worker id keeps only that worker's row.
//
// onNavigate (the app's navigate) lets a tool card that ran monoagentcli on
// one of the app's tabs link to it (#473).
// onA2UIAction(actionEnvelope): CALL SITE for a pressed A2UI button. The
// envelope is the v1.0 {version, action:{name,surfaceId,sourceComponentId,
// timestamp,context}} object; the chat view will pass a Wails binding here
// (CoderChatView passes useA2UIActions). surfaces: this turn's cards after the
// conversation-wide fold (default: the turn's own). Default: the renderer logs it.
// entityLinks: false when the view already shows result cards (the coder
// chat's artifact cards), so a card does not also link its entity.
export function ChatTimeline({ state, turnId = '', isLive = true, agentFilter = null, onNavigate, entityLinks = true, onA2UIAction, surfaces: conversationSurfaces }) {
  const { parts, calls } = state
  const notices = state.notices || []
  const allSurfaces = conversationSurfaces || state.surfaces || []
  // A filter hides the cards, but never one that waits for the person: those are answered together.
  const waits = sf => sf.status !== 'resolved' && hasButton(sf) && (isAskCard(sf) || sf.provenance === 'agent')
  const surfaces = agentFilter && agentFilter !== 'lead' ? allSurfaces.filter(waits) : allSurfaces
  if (parts.length === 0 && notices.length === 0 && !state.sandbox && surfaces.length === 0) return null
  const surfacesAt = (i, last) => surfaces.filter(sf => (last ? sf.partIndex >= i : sf.partIndex === i)).map(sf => (
    <A2UISurface key={`a2ui-${sf.surfaceId}`} surface={sf} onAction={onA2UIAction} group={turnId} live={!state.terminal} />
  ))
  // A worker's question is answered in its card, not in its row, when the card
  // holds several questions or anything else of the turn waits with it.
  const openCards = state.terminal ? [] : allSurfaces.filter(waits)
  const answerInCard = agent => {
    const card = agent?.question && openCards.find(sf => sf.surfaceId === `ask-${agent.agentId}.${agent.question.id}`)
    return !!card && (openCards.length > 1 || !!card.components['q-q1'])
  }

  const childrenOf = {}
  for (const part of parts) {
    const call = part.kind === 'tool' ? calls[part.callId] : null
    if (call?.parentCallId && calls[call.parentCallId]) {
      (childrenOf[call.parentCallId] ||= []).push(call)
    }
  }
  const renderCall = (call) => (
    <ToolActivityCard
      key={`tool-${call.callId}`}
      call={call}
      turnId={turnId}
      isLive={isLive}
      childCalls={childrenOf[call.callId] || []}
      renderChild={renderCall}
      onNavigate={onNavigate}
      entityLinks={entityLinks}
    />
  )

  const renderPart = (part, i) => {
    if (agentFilter && (part.kind === 'agent' ? agentFilter === 'lead' || part.agentId !== agentFilter : agentFilter !== 'lead')) return null
    if (part.kind === 'text') {
      return part.text ? (
        <ChatBlocksScope.Provider key={`text-${part.partId}-${i}`} value={{ scope: `${turnId}:${part.partId}`, turn: turnId, live: isLive && !state.terminal }}>
          <ChatMarkdown content={part.text} onNavigate={onNavigate} />
        </ChatBlocksScope.Provider>
      ) : null
    }
    if (part.kind === 'agent') {
      const agent = (state.agents || {})[part.agentId]
      return <AgentRow key={`agent-${part.agentId}`} agent={agent} scope={state.scope} answerInCard={answerInCard(agent)} />
    }
    const call = calls[part.callId]
    if (!call || childrenOf[call.parentCallId]) return null
    return renderCall(call)
  }

  // A coder turn's cards take the turn's full width (commands and paths
  // are long), and so does a turn with A2UI cards (they have forms to read;
  // a2ui.css caps them); other turns keep sizing to their content.
  const hasNative = Object.values(calls).some(c => c.native)
  const statusNotices = notices.filter(n => n.code === 'coder.status')
  const lastStatus = statusNotices[statusNotices.length - 1]?.message || ''
  // "Ready. Still connecting: X. Not available: Y (needs-auth)" is worth
  // keeping after startup; a plain "Ready" is not.
  const readyNote = /^Ready\b/.test(lastStatus) && !/^Ready\.?$/.test(lastStatus.trim()) ? lastStatus : ''
  const showStatus = isLive && !state.terminal && parts.length === 0 && !!lastStatus && !/^Ready\b/.test(lastStatus)

  return (
    <div data-testid="chat-timeline" style={hasNative || surfaces.length > 0 ? { alignSelf: 'stretch' } : undefined}>
      {state.sandbox && <SandboxBadge status={state.sandbox} detail={state.sandboxDetail} />}
      {state.sandboxMissing && <UnsandboxedWarning />}
      {notices.filter(n => n.code === 'coder.workspace').slice(-1).map((notice, i) => (
        <div key={`ws-${i}`} data-testid="coder-workspace-line" style={{ display: 'flex', alignItems: 'center', gap: 5, marginTop: 4, fontFamily: 'var(--font-mono)', fontSize: 9.5, color: 'var(--text-muted)', wordBreak: 'break-all' }}>
          <FolderOpen size={10} style={{ flexShrink: 0 }} />
          {notice.message}
        </div>
      ))}
      {readyNote && (
        <div data-testid="coder-ready-note" style={{ display: 'flex', alignItems: 'flex-start', gap: 5, marginTop: 4, fontFamily: 'var(--font-mono)', fontSize: 9.5, color: 'var(--text-muted)' }}>
          <Info size={10} style={{ flexShrink: 0, marginTop: 1 }} />
          {readyNote}
        </div>
      )}
      {/* One list, so a card keeps its place in it (and what was typed into it) when parts arrive after it. */}
      {parts.flatMap((part, i) => [...surfacesAt(i, false), renderPart(part, i)]).concat(surfacesAt(parts.length, true))}
      {showStatus && (
        <div data-testid="coder-status-line" style={{ display: 'flex', alignItems: 'center', gap: 6, marginTop: 6, fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)' }}>
          <Loader size={10} className="chat-spin" style={{ color: '#00b4d8' }} />
          {lastStatus}
        </div>
      )}
      {notices.map((notice, i) => {
        if (notice.code === 'coder.status' || notice.code === 'coder.workspace') return null
        if (notice.code === 'coder.background') return <CoderBackgroundBanner key={i} notice={notice} />
        return <NoticeBanner key={i} notice={notice} />
      })}
    </div>
  )
}
