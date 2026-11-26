// Shared presentational bits for the Connections page (browser automations
// and API connections). Inline-style language matches the rest of the app:
// mono labels, CSS vars, small bordered chips.
import { useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import i18n from 'i18next'
import { Loader } from 'lucide-react'
import i18nOr from '../../lib/i18nOr.js'

export const mono = { fontFamily: 'var(--font-mono)' }
export const label = { fontFamily: 'var(--font-mono)', fontSize: 9.5, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: 1.2 }
export const muted = { fontFamily: 'var(--font-mono)', fontSize: 10.5, color: 'var(--text-muted)' }
export const body = { fontFamily: 'var(--font-body)', fontSize: 12, color: 'var(--text-secondary)', lineHeight: 1.5 }
export const panel = { background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius-lg)', padding: '10px 14px' }
export const spin = { animation: 'spin .7s linear infinite' }

// Display names of values the CLI sends; the values themselves stay untouched.
export const SOURCE_LABELS = {
  get builtin() { return i18nOr('ui6Conn.source_builtin') },
  get imported() { return i18nOr('ui6Conn.source_imported') },
  get local() { return i18nOr('ui6Conn.source_local') },
  get recorded() { return i18nOr('ui6Conn.source_recorded') },
  monoes: 'monoes.me',
}
export const trustName = (v) => v && i18nOr(`ui6Conn.trust_value_${v}`, { defaultValue: v })
export const tierName = (v) => v && i18nOr(`ui6Conn.tier_value_${v}`, { defaultValue: v })

// Colours for an action's declared side effects (spec §4.3).
export const EFFECT_COLORS = {
  none: 'var(--text-muted)',
  read: 'var(--cyan)',
  write: 'var(--yellow)',
  message: 'var(--orange)',
  destructive: 'var(--red)',
}

// isRisky: an action needs a confirmation before it runs for real unless it
// explicitly declares no or read-only side effects (missing = dangerous).
export function isRisky(effect) {
  return effect !== 'none' && effect !== 'read'
}

const FOCUSABLE = 'button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), summary, [tabindex]:not([tabindex="-1"])'

// useDialog gives a drawer or dialog modal keyboard behaviour: focus moves
// into it on open and back to the opener on close, Tab stays inside, and
// Escape closes it. The key handler sits on the element itself (spread the
// returned props onto it), so an Escape meant for a confirm dialog opened
// on top never reaches it.
export function useDialog(onClose) {
  const ref = useRef(null)
  const closeRef = useRef(onClose)
  closeRef.current = onClose
  useEffect(() => {
    const opener = document.activeElement
    const el = ref.current
    const first = el?.querySelector(FOCUSABLE)
    ;(first || el)?.focus?.()
    return () => { if (opener && document.contains(opener)) opener.focus?.() }
  }, [])
  const onKeyDown = (e) => {
    if (e.key === 'Escape') {
      e.stopPropagation()
      closeRef.current?.()
      return
    }
    if (e.key !== 'Tab' || !ref.current) return
    const items = [...ref.current.querySelectorAll(FOCUSABLE)].filter(x => x.offsetParent !== null || x === document.activeElement)
    if (!items.length) return
    const first = items[0], last = items[items.length - 1]
    if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus() }
    else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus() }
  }
  return { ref, onKeyDown, tabIndex: -1 }
}

export function Chip({ children, color = 'var(--text-muted)', title, style }) {
  return (
    <span
      title={title}
      style={{
        display: 'inline-flex', alignItems: 'center', gap: 3,
        fontFamily: 'var(--font-mono)', fontSize: 9, color, lineHeight: '15px',
        border: `1px solid ${color}`, borderRadius: 8, padding: '0 6px', whiteSpace: 'nowrap',
        ...style,
      }}
    >
      {children}
    </span>
  )
}

export function EffectChip({ effect }) {
  const { t } = useTranslation()
  const e = effect || 'none'
  const text = t(`ui6Conn.effect_value_${e}`, { defaultValue: e })
  return <Chip color={EFFECT_COLORS[e] || 'var(--text-muted)'} title={t('ui6Conn.side_effects', { effect: text })}>{text}</Chip>
}

export function Dot({ on, warn }) {
  const color = on ? 'var(--green-neon)' : warn ? 'var(--yellow)' : 'var(--text-muted)'
  return <span aria-hidden="true" style={{ width: 7, height: 7, borderRadius: '50%', flexShrink: 0, background: color, boxShadow: on ? '0 0 5px var(--green-neon)' : 'none' }} />
}

export function ErrorBox({ children }) {
  if (!children) return null
  return (
    <div role="alert" style={{ ...mono, fontSize: 11, padding: '7px 12px', borderRadius: 'var(--radius)', background: 'rgba(239,68,68,.08)', border: '1px solid rgba(239,68,68,.25)', color: 'var(--red)', whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>
      {children}
    </div>
  )
}

export function OkBox({ children }) {
  if (!children) return null
  return (
    <div role="status" style={{ ...mono, fontSize: 11, padding: '7px 12px', borderRadius: 'var(--radius)', background: 'rgba(74,222,128,.08)', border: '1px solid rgba(74,222,128,.25)', color: 'var(--green-neon)', wordBreak: 'break-word' }}>
      {children}
    </div>
  )
}

export function Busy({ text }) {
  const { t } = useTranslation()
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 6, ...muted }}>
      <Loader size={11} style={spin} /> {text ?? t('ui6Conn.working')}
    </div>
  )
}

export function SectionHeader({ title, hint, count }) {
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 12 }}>
      <span style={{ ...mono, fontSize: 11, fontWeight: 700, color: 'var(--text)', textTransform: 'uppercase', letterSpacing: 2 }}>{title}</span>
      {hint && <span style={{ ...muted, fontSize: 10 }}>{hint}</span>}
      <div style={{ flex: 1, height: 1, background: 'var(--border)' }} />
      {count && <span style={{ ...mono, fontSize: 9.5, color: 'var(--text-muted)' }}>{count}</span>}
    </div>
  )
}

export function KV({ rows }) {
  return (
    <div style={{ ...panel, display: 'flex', flexDirection: 'column', gap: 7 }}>
      {rows.filter(Boolean).map(([k, v]) => (
        <div key={k} style={{ display: 'flex', justifyContent: 'space-between', gap: 12 }}>
          <span style={label}>{k}</span>
          <span style={{ ...mono, fontSize: 11, color: 'var(--text-secondary)', textAlign: 'right', wordBreak: 'break-all' }}>{v ?? '—'}</span>
        </div>
      ))}
    </div>
  )
}

// Dates follow the app language; English keeps the US format it always had.
export const dateLocale = () => (i18n.language?.startsWith('es') ? 'es' : 'en-US')

export function fmtDate(s) {
  if (!s || s.startsWith('0001-')) return '—'
  try { return new Date(s).toLocaleString(dateLocale(), { month: 'short', day: 'numeric', year: 'numeric', hour: '2-digit', minute: '2-digit' }) }
  catch { return s.slice(0, 16) }
}

export function fmtShort(s) {
  if (!s || s.startsWith('0001-')) return '—'
  try { return new Date(s).toLocaleDateString(dateLocale(), { month: 'short', day: 'numeric' }) }
  catch { return s.slice(0, 10) }
}

// copyText copies via the Wails runtime when present, else the browser API.
export async function copyText(text) {
  try {
    if (window.runtime?.ClipboardSetText) return await window.runtime.ClipboardSetText(text)
    await navigator.clipboard?.writeText(text)
    return true
  } catch { return false }
}

// fileSize renders bytes for the install review file list.
export function fileSize(n) {
  if (n == null) return '—'
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}
