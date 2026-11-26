import { AlertTriangle, Info, AlertCircle, Loader, FolderOpen } from 'lucide-react'
import { ChatMarkdown } from './ChatMarkdown.jsx'
import { ToolActivityCard } from './ToolActivityCard.jsx'
import { CoderBackgroundBanner } from './CoderBackgroundBanner.jsx'
import { SandboxBadge } from './SandboxBadge.jsx'
import { UnsandboxedWarning } from './ChatWarnings.jsx'
import { AgentRow } from './AgentRow.jsx'
import { ChatBlocksScope } from './blocks/context.js'

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
// entityLinks: false when the view already shows result cards (the coder
// chat's artifact cards), so a card does not also link its entity.
export function ChatTimeline({ state, turnId = '', isLive = true, agentFilter = null, onNavigate, entityLinks = true }) {
  const { parts, calls } = state
  const notices = state.notices || []
  if (parts.length === 0 && notices.length === 0 && !state.sandbox) return null

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

  // A coder turn's cards take the turn's full width (commands and paths
  // are long); other turns keep sizing to their content.
  const hasNative = Object.values(calls).some(c => c.native)
  const statusNotices = notices.filter(n => n.code === 'coder.status')
  const lastStatus = statusNotices[statusNotices.length - 1]?.message || ''
  // "Ready. Still connecting: X. Not available: Y (needs-auth)" is worth
  // keeping after startup; a plain "Ready" is not.
  const readyNote = /^Ready\b/.test(lastStatus) && !/^Ready\.?$/.test(lastStatus.trim()) ? lastStatus : ''
  const showStatus = isLive && !state.terminal && parts.length === 0 && !!lastStatus && !/^Ready\b/.test(lastStatus)

  return (
    <div data-testid="chat-timeline" style={hasNative ? { alignSelf: 'stretch' } : undefined}>
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
      {parts.map((part, i) => {
        if (agentFilter && (part.kind === 'agent' ? agentFilter === 'lead' || part.agentId !== agentFilter : agentFilter !== 'lead')) return null
        if (part.kind === 'text') {
          return part.text ? (
            <ChatBlocksScope.Provider key={`text-${part.partId}-${i}`} value={{ scope: `${turnId}:${part.partId}`, live: isLive && !state.terminal }}>
              <ChatMarkdown content={part.text} onNavigate={onNavigate} />
            </ChatBlocksScope.Provider>
          ) : null
        }
        if (part.kind === 'agent') return <AgentRow key={`agent-${part.agentId}`} agent={(state.agents || {})[part.agentId]} scope={state.scope} />
        const call = calls[part.callId]
        if (!call || childrenOf[call.parentCallId]) return null
        return renderCall(call)
      })}
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
