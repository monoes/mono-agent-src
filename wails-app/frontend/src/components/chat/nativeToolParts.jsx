import { Copy } from 'lucide-react'
import { copyToClipboard } from './toolCardUtils.js'
import { diffLines } from './unifiedDiff.js'
import { maskSecretShapes } from './credentials.js'
import i18nOr from '../../lib/i18nOr.js'

// Shared building blocks of the native tool cards (NativeToolCard.jsx,
// nativeToolDescribe.jsx): tags, labelled sections, the result block and
// the two diff views.

export const mono = 'var(--font-mono)'
export const CYAN = '#00b4d8'
export const preStyle = {
  margin: 0, fontFamily: mono, fontSize: 10, color: '#94a3b8',
  whiteSpace: 'pre-wrap', wordBreak: 'break-word', maxHeight: 220, overflow: 'auto',
}
export const sectionLabel = { fontFamily: mono, fontSize: 8, color: 'var(--text-muted)', letterSpacing: 1.5, textTransform: 'uppercase' }

export function Tag({ children, color = 'var(--text-muted)', testId }) {
  return (
    <span data-testid={testId} style={{
      fontFamily: mono, fontSize: 8.5, color, border: `1px solid ${color}55`, borderRadius: 4,
      padding: '0 4px', flexShrink: 0, letterSpacing: 0.5,
    }}>
      {children}
    </span>
  )
}

export function Section({ label, copyText, children }) {
  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 3 }}>
        <span style={sectionLabel}>{label}</span>
        {copyText != null && (
          <button type="button" onClick={() => copyToClipboard(copyText)} title={i18nOr('ui7Chat.copy_named', { what: label.toLowerCase() })} aria-label={i18nOr('ui7Chat.copy_named', { what: label.toLowerCase() })}
            style={{ background: 'transparent', border: 'none', cursor: 'pointer', color: 'var(--text-muted)', padding: 0, display: 'flex' }}>
            <Copy size={9} />
          </button>
        )}
      </div>
      {children}
    </div>
  )
}

// ResultSection is a call's output, with token-shaped secrets in it masked
// (credentials.js), in the copy too.
export function ResultSection({ call, label = i18nOr('ui7Chat.output') }) {
  if (call.status !== 'completed') return null
  const result = maskSecretShapes(call.result ?? '')
  const text = result === '' ? i18nOr('ui7Chat.no_output') : result
  return (
    <Section label={label} copyText={result}>
      <pre style={{ ...preStyle, color: call.ok === false ? '#fca5a5' : preStyle.color }}>{text}</pre>
    </Section>
  )
}

const diffBox = { ...preStyle, whiteSpace: 'pre', background: '#01040a', borderRadius: 4, padding: '4px 0' }

function DiffRow({ type, text }) {
  return (
    <div data-diff={type === ' ' ? 'context' : type === '+' ? 'add' : type === '-' ? 'del' : 'hunk'} style={{
      padding: '0 8px',
      color: type === '+' ? '#86efac' : type === '-' ? '#fca5a5' : type === '@' ? CYAN : '#64748b',
      background: type === '+' ? 'rgba(16,185,129,0.08)' : type === '-' ? 'rgba(239,68,68,0.08)' : 'transparent',
    }}>
      {type === '@' ? text : `${type}${text}`}
    </div>
  )
}

// DiffView is an edit's old → new as unified-diff lines.
export function DiffView({ oldText, newText }) {
  const lines = diffLines(oldText, newText)
  return (
    <div data-testid="edit-diff" style={diffBox}>
      {lines.map((l, i) => <DiffRow key={i} type={l.type} text={l.text} />)}
    </div>
  )
}

// patchLines reads a unified diff text (a patch's per-file diff) into
// rows: "@@" hunk headers, +/- lines and context; the ---/+++ file header
// lines are dropped, since the card already names the file.
export function patchLines(diff) {
  const out = []
  for (const line of String(diff ?? '').replace(/\n$/, '').split('\n')) {
    if (line.startsWith('+++') || line.startsWith('---') || line.startsWith('diff ') || line.startsWith('index ')) continue
    if (line.startsWith('@@')) out.push({ type: '@', text: line })
    else if (line.startsWith('+') || line.startsWith('-') || line.startsWith(' ')) out.push({ type: line[0], text: line.slice(1) })
    else if (line !== '') out.push({ type: ' ', text: line })
  }
  return out
}

// PatchDiffView renders one file's unified diff text as it came.
export function PatchDiffView({ diff }) {
  return (
    <div data-testid="patch-diff" style={diffBox}>
      {patchLines(diff).map((l, i) => <DiffRow key={i} type={l.type} text={l.text} />)}
    </div>
  )
}
