import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

const mono = 'var(--font-mono)'
const note = { fontFamily: mono, fontSize: 9.5, color: 'var(--text-muted)', overflowWrap: 'anywhere' }
const warning = { ...note, color: '#fbbf24' }
const column = { display: 'flex', flexDirection: 'column', gap: 3, marginTop: 4 }
const box = { margin: 0, maxHeight: 160, overflow: 'auto', whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', fontFamily: mono, fontSize: 11, color: 'var(--text)', background: 'var(--elevated)', border: '1px solid var(--border)', borderRadius: 'var(--radius)', padding: '6px 8px' }

// Characters that show nothing or reorder what shows: controls (tab and line
// break aside), format characters (bidi, zero width, tags) and the Unicode
// line and paragraph separators.
const unseen = /([^\P{Cc}\t\n]|[\p{Cf}\p{Zl}\p{Zp}])/u

const escapeOf = ch => {
  const hex = ch.codePointAt(0).toString(16).toUpperCase().padStart(4, '0')
  return hex.length > 4 ? `\\u{${hex}}` : `\\u${hex}`
}

// visible is text with each unseen character written as a marked escape.
const visible = text => text.split(unseen).map((part, i) => (i % 2
  ? <span key={i} data-testid="approval-escape" style={{ color: '#fbbf24', border: '1px solid #fbbf24', borderRadius: 2 }}>{escapeOf(part)}</span>
  : part))

// The lines the 160px box shows whole, for when it has no size to measure.
const BOX_LINES = 8

// useMoreBelow says whether the box holds more than shows under its edge.
function useMoreBelow(text) {
  const ref = useRef(null)
  const [more, setMore] = useState(false)
  const measure = () => {
    const el = ref.current
    if (!el) return
    setMore(el.clientHeight > 0
      ? el.scrollHeight - el.scrollTop - el.clientHeight > 1
      : text.split('\n').length > BOX_LINES)
  }
  useLayoutEffect(measure, [text])
  useEffect(() => {
    if (!ref.current || typeof ResizeObserver === 'undefined') return undefined
    const observer = new ResizeObserver(measure)
    observer.observe(ref.current)
    return () => observer.disconnect()
  }, [text])
  return { ref, more, measure }
}

// monomind keeps the first 200 UTF-16 units of each string argument and adds
// "…" (internal/orgchat/command.go).
const isCut = value => typeof value === 'string' && value.length === 201 && value.endsWith('\u2026')

// ApprovalInput is a call's arguments as monomind recorded them, with the same
// escapes and warnings as a command.
export function ApprovalInput({ input }) {
  const { t } = useTranslation()
  const text = input && Object.keys(input).length > 0 ? JSON.stringify(input, null, 2) : ''
  const { ref, more, measure } = useMoreBelow(text)
  if (!text) return null
  return (
    <div style={column}>
      <pre ref={ref} onScroll={measure} data-testid="approval-input" aria-label={t('ask.inputLabel')} tabIndex={0} style={box}>{visible(text)}</pre>
      {more && <div role="note" style={warning}>{t('ask.inputMore')}</div>}
      {Object.values(input).some(isCut) && <div role="note" style={warning}>{t('ask.inputCut')}</div>}
      {text.includes('[REDACTED]') && <div role="note" style={warning}>{t('ask.inputRedacted')}</div>}
    </div>
  )
}

// ApprovalCommand is what an approval of a shell call would run: the command,
// the folder the org file gives the role, and warnings when only the start of
// the command is known, when monomind hid part of it and when the box does not
// show it all. The first three come from the CLI (`org approvals`, `org chat
// history`); a call with no command shows nothing.
export function ApprovalCommand({ command, truncated, cwd }) {
  const { t } = useTranslation()
  const { ref, more, measure } = useMoreBelow(command || '')
  if (!command) return null
  return (
    <div data-testid="approval-command" style={column}>
      <pre ref={ref} onScroll={measure} aria-label={t('ask.commandLabel')} tabIndex={0} style={box}>{visible(command)}</pre>
      {more && <div role="note" style={warning}>{t('ask.commandMore')}</div>}
      {cwd && <div style={note}>{visible(t('ask.commandFolder', { path: cwd }))}</div>}
      {truncated && <div role="note" style={warning}>{t('ask.commandCut')}</div>}
      {command.includes('[REDACTED]') && <div role="note" style={warning}>{t('ask.commandRedacted')}</div>}
    </div>
  )
}

// ApprovalCall is all that an Approve releases: the command, if the call has
// one, and every argument the command box does not already show.
export function ApprovalCall({ command, truncated, cwd, input }) {
  let others = input
  if (command && input?.command === command) {
    const { command: shown, ...rest } = input
    others = rest
  }
  return (
    <>
      <ApprovalCommand command={command} truncated={truncated} cwd={cwd} />
      <ApprovalInput input={others} />
    </>
  )
}
