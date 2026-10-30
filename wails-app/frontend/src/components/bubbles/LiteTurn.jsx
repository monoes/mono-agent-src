import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Check, X, Loader, ChevronRight, ChevronDown } from 'lucide-react'
import { ChatMarkdown } from '../chat/ChatMarkdown.jsx'
import { maskArgs, maskSecretShapes } from '../chat/credentials.js'
import { vaultRelatedCall } from '../chat/toolLinks.js'

const HINT_KEYS = ['command', 'file_path', 'path', 'pattern', 'query', 'url', 'description']

// hintOf is the short target of a call ("go test ./...", "src/app.go"), masked, or ''.
function hintOf(call) {
  if (vaultRelatedCall(call)) return ''
  const args = call.arguments
  if (!args || typeof args !== 'object') return ''
  const key = HINT_KEYS.find(k => typeof args[k] === 'string' && args[k])
  return key ? maskSecretShapes(args[key]).replace(/\s+/g, ' ').slice(0, 60) : ''
}

const text = v => (v == null ? '' : typeof v === 'string' ? v : JSON.stringify(v, null, 2))

// ToolChip is one tool call as a one-line chip; it opens to the call's arguments
// and result. Text is rendered by React as text, never as markup.
function ToolChip({ call, isLive, turnId }) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const withheld = vaultRelatedCall(call)
  const failed = call.status === 'completed' && call.ok === false
  const running = isLive && call.status === 'started'
  const status = failed ? 'failed' : running ? 'running' : call.status === 'started' ? 'interrupted' : 'done'
  const Icon = failed ? X : running ? Loader : Check
  const hint = hintOf(call)
  const id = `lite-call-${turnId}-${call.callId}`
  return (
    <div className="lite-chip" data-status={status}>
      <button type="button" aria-expanded={open} aria-controls={id} onClick={() => setOpen(o => !o)}>
        {open ? <ChevronDown size={10} /> : <ChevronRight size={10} />}
        <Icon size={10} className={running ? 'chat-spin' : undefined} aria-hidden="true" />
        <span className="lite-chip-name">{call.name}{hint ? ` · ${hint}` : ''}</span>
        <span style={{ position: 'absolute', width: 1, height: 1, overflow: 'hidden', clip: 'rect(0,0,0,0)' }}>{t(`bubbles.lite.call.${status}`)}</span>
      </button>
      <div id={id} className="lite-chip-body" hidden={!open}>
        {open && (withheld
          ? <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)' }}>{t('toolLinks.vaultHidden')}</span>
          : <pre style={{ margin: 0, padding: '6px 8px', whiteSpace: 'pre-wrap', wordBreak: 'break-word', fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-secondary)', background: 'var(--base)', border: '1px solid var(--border)', borderRadius: 6, maxHeight: 220, overflow: 'auto' }}>
            {text(maskArgs(call.arguments))}
            {call.status === 'completed' ? `\n\n${text(maskSecretShapes(String(call.result ?? '')))}` : ''}
          </pre>)}
      </div>
    </div>
  )
}

// LiteTurn renders one turn for a lite pane: the assistant's text, and every tool call
// as a chip. Nested calls (made inside a subagent call) are left out; the root pane has them.
export function LiteTurn({ state, turnId = '', isLive = false }) {
  const parts = state?.parts || []
  const calls = state?.calls || {}
  return (
    <div data-testid="lite-turn" style={{ maxWidth: '100%', minWidth: 0 }}>
      {parts.map((part, i) => {
        if (part.kind === 'text') return part.text ? <ChatMarkdown key={`t-${part.partId}-${i}`} content={part.text} /> : null
        if (part.kind !== 'tool') return null
        const call = calls[part.callId]
        if (!call || (call.parentCallId && calls[call.parentCallId])) return null
        return <ToolChip key={`c-${call.callId}`} call={call} isLive={isLive} turnId={turnId} />
      })}
    </div>
  )
}
