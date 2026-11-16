import { ChatMarkdown } from './ChatMarkdown.jsx'
import { isAgentNotSetup, withoutAgentSetupMarker } from '../../lib/agentSetup.js'
import AgentSetupLink from '../AgentSetupLink.jsx'

// ── Message bubble ─────────────────────────────────────────────────────────────
// Exported (like FileViewerModal's fileViewerKind) so the markdown-vs-plain
// rendering choice is directly unit-testable without driving the whole
// panel's streaming/session machinery. Reachable today only for 'user' and
// 'error' roles — the reducer/ChatTimeline path (role:'turn') fully
// replaced the old assistant-bubble-plus-inline-tool-cards rendering this
// component used to also handle; a prior version's now-dead `toolCalls`
// prop and its ToolCallCard were removed for exactly that reason (nothing
// ever constructed a message carrying one).
export function MessageBubble({ role, content, isError, code, onNavigate }) {
  const isUser = role === 'user'
  return (
    <div style={{
      display: 'flex',
      flexDirection: 'column',
      alignItems: isUser ? 'flex-end' : 'flex-start',
      marginBottom: 8,
    }}>
      <div style={{
        maxWidth: '88%',
        padding: '8px 12px',
        borderRadius: isUser ? '12px 12px 4px 12px' : '12px 12px 12px 4px',
        background: isError
          ? 'rgba(239,68,68,0.1)'
          : isUser
            ? 'rgba(0,180,216,0.15)'
            : '#0d1a28',
        border: isError
          ? '1px solid rgba(239,68,68,0.25)'
          : isUser
            ? '1px solid rgba(0,180,216,0.25)'
            : '1px solid rgba(0,180,216,0.08)',
        color: isError ? '#fca5a5' : '#e2e8f0',
      }}>
        {isUser ? (
          // Raw pre-wrap text — never reinterpreted as markdown syntax the
          // user didn't intend (e.g. a typed "1. foo" becoming a list).
          <div style={{ fontFamily: 'var(--font-mono)', fontSize: 11, lineHeight: 1.55, wordBreak: 'break-word', whiteSpace: 'pre-wrap' }}>
            {content}
          </div>
        ) : (
          // Error text renders through the same ChatMarkdown ChatTimeline
          // uses for assistant text — scheme-gated links, no raw HTML, no
          // auto-loaded images — rather than a second, less-careful
          // markdown config (an error string ultimately traces back to a
          // subprocess failure message, not first-party copy).
          <ChatMarkdown content={withoutAgentSetupMarker(content)} onNavigate={onNavigate} />
        )}
        {isError && isAgentNotSetup({ code, message: content }) && <AgentSetupLink onNavigate={onNavigate} />}
      </div>
    </div>
  )
}
