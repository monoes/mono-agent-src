import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Palette, SwatchBook, Wrench, Bug, Plug, MessageCircleQuestion, FolderOpen, X, Play, Loader } from 'lucide-react'
import { api } from '../../services/api.js'
import { folderName } from '../chat/useCoderMode.js'
import { FullAccessWarning } from '../chat/ChatWarnings.jsx'

// Queued coder chats (C5 of docs/mastermind/plans/2026-10-15-coder-browser.md):
// the browser extension queues a coder chat with a page's context, and it
// waits here, next to the bubble dock, until the person presses Start (the
// extension never starts a full-access turn itself). Strings use
// defaultValue until the coderQueue.* keys are in the locale files.

const mono = 'var(--font-mono)'
const SHOWN = 2

const kindIcon = {
  'copy-style': Palette,
  tokens: SwatchBook,
  'fix-element': Wrench,
  'bug-report': Bug,
  integration: Plug,
  ask: MessageCircleQuestion,
}
const kindLabel = {
  'copy-style': 'Copy style',
  tokens: 'Design tokens',
  'fix-element': 'Fix element',
  'bug-report': 'Bug report',
  integration: 'Integration',
  ask: 'Question',
}

// useQueuedCoderChats is the active profile's queued chats, listed again
// whenever the app sees the queue folder change (coder:queue). It goes
// through the api object only, so a page without the queue bindings still
// renders the dock.
export function useQueuedCoderChats() {
  const [items, setItems] = useState([])
  const refresh = useCallback(() => {
    Promise.resolve().then(() => api.coderQueueList())
      .then(res => setItems(Array.isArray(res?.items) ? res.items.filter(it => it.status === 'queued') : []))
      .catch(() => {})
  }, [])
  useEffect(() => {
    refresh()
    const off = api.onCoderQueue?.(refresh)
    return () => off?.()
  }, [refresh])
  const drop = useCallback(id => setItems(list => list.filter(it => it.id !== id)), [])
  return { items, refresh, drop }
}

// pageAddress is the whole address the chat was queued from (origin and
// path, as the browser reported them), shown in full so the person sees
// which site the agent will be told about before pressing Start.
function pageAddress(item) {
  return cleanLine(`${item.origin || ''}${item.path || ''}`)
}

// cleanLine shows page text the way the CLI stores it (cleanLine in
// internal/cli/chat/coder_queue_page.go): format characters (bidi controls,
// zero-width characters: \p{Cf}) dropped, so a page can't reorder what the
// card shows, and runs of spaces and control characters as one space.
export function cleanLine(s) {
  return String(s || '').replace(/\p{Cf}/gu, '').replace(/[\s\p{Cc}]+/gu, ' ').trim()
}

// QueuedCoderBubble is one queued chat: its kind, the page, a screenshot,
// the folder it will work in (changeable), and Start / Dismiss. onStart
// gets the folder ('' = the one it was queued for) and rejects with the
// reason it could not start.
export function QueuedCoderBubble({ item, onStart, onDismiss }) {
  const { t } = useTranslation()
  const [thumb, setThumb] = useState('')
  const [cwd, setCwd] = useState('')
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const Icon = kindIcon[item.kind] || MessageCircleQuestion
  const kind = t(`coderQueue.kind.${item.kind}`, { defaultValue: kindLabel[item.kind] || item.kind })
  const folder = cwd || item.cwd || ''
  const title = cleanLine(item.title)

  useEffect(() => {
    let live = true
    Promise.resolve().then(() => api.coderQueueImage(item.id))
      .then(res => { if (live && typeof res?.dataUrl === 'string' && res.dataUrl.startsWith('data:image/')) setThumb(res.dataUrl) })
      .catch(() => {})
    return () => { live = false }
  }, [item.id])

  const pickFolder = async () => {
    const dir = await Promise.resolve(api.pickCoderFolder()).catch(() => '')
    if (typeof dir === 'string' && dir && !dir.startsWith('{')) setCwd(dir)
  }
  const run = (what, fn) => async () => {
    if (busy) return
    setBusy(what)
    setError('')
    try {
      await fn()
    } catch (err) {
      setError(String(err?.message || err))
      setBusy('')
    }
  }

  return (
    <div role="group" data-testid="queued-coder" data-queue-id={item.id}
      aria-label={t('coderQueue.label', { defaultValue: 'Queued coder chat: {{kind}}', kind })}
      style={{
        width: 272, padding: '10px 12px', borderRadius: 'var(--radius-lg)', border: '1px solid var(--border-bright)',
        background: 'var(--overlay)', boxShadow: '0 10px 28px rgba(0, 0, 0, 0.5)', fontFamily: mono, fontSize: 10,
        color: 'var(--text-secondary)', display: 'flex', flexDirection: 'column', gap: 6, animation: 'fade-in 140ms ease-out',
      }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 7 }}>
        <span aria-hidden="true" style={{ width: 22, height: 22, borderRadius: '50%', display: 'flex', alignItems: 'center', justifyContent: 'center', background: 'var(--cyan-glow-strong)', color: 'var(--cyan-bright)', flexShrink: 0 }}>
          <Icon size={12} />
        </span>
        <span style={{ fontFamily: 'var(--font-display)', fontSize: 11.5, fontWeight: 600, color: 'var(--text)', whiteSpace: 'nowrap' }}>{kind}</span>
        <span style={{ fontSize: 9, color: 'var(--text-dim)', border: '1px solid var(--border)', borderRadius: 999, padding: '0 6px', whiteSpace: 'nowrap' }}>
          {t('coderQueue.fromBrowser', { defaultValue: 'from the browser' })}
        </span>
        <span style={{ flex: 1 }} />
        <button type="button" className="btn btn-ghost btn-sm" onClick={run('dismiss', () => onDismiss(item))} disabled={!!busy}
          aria-label={t('coderQueue.dismissNamed', { defaultValue: 'Dismiss {{kind}}', kind })} title={t('coderQueue.dismiss', { defaultValue: 'Dismiss' })}
          style={{ padding: '2px 4px' }}>
          <X size={12} />
        </button>
      </div>
      {title && (
        <div data-testid="queued-title" title={title} style={{ fontSize: 11, color: 'var(--text)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{title}</div>
      )}
      <div data-testid="queued-page" style={{ color: 'var(--cyan)', overflowWrap: 'anywhere', wordBreak: 'break-all', lineHeight: 1.35 }}>{pageAddress(item)}</div>
      {thumb && (
        <img src={thumb} alt={t('coderQueue.screenshot', { defaultValue: 'Screenshot of the page' })}
          style={{ width: '100%', height: 84, objectFit: 'cover', objectPosition: 'top', borderRadius: 6, border: '1px solid var(--border)' }} />
      )}
      {item.note && (
        <div style={{ color: 'var(--text-muted)', display: '-webkit-box', WebkitLineClamp: 2, WebkitBoxOrient: 'vertical', overflow: 'hidden' }}>“{item.note}”</div>
      )}
      <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
        <FolderOpen size={11} aria-hidden="true" style={{ flexShrink: 0, color: 'var(--text-dim)' }} />
        <span data-testid="queued-folder" title={folder} style={{ flex: 1, minWidth: 0, color: 'var(--text)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
          {folderName(folder) || t('coderQueue.coderRoot', { defaultValue: 'coder root' })}
        </span>
        <button type="button" className="btn btn-ghost btn-sm" onClick={pickFolder} disabled={!!busy} style={{ fontSize: 9.5, padding: '1px 6px' }}>
          {t('coderQueue.changeFolder', { defaultValue: 'Change' })}
        </button>
      </div>
      {/* Start makes a full-access chat (`coder queue start`). A status: this card's alert is its error. */}
      <div style={{ margin: '0 -12px' }}><FullAccessWarning role="status" /></div>
      {error && <div role="alert" style={{ color: 'var(--red)', lineHeight: 1.4 }}>{error}</div>}
      <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 6 }}>
        <button type="button" className="btn btn-ghost btn-sm" onClick={run('dismiss', () => onDismiss(item))} disabled={!!busy} style={{ fontSize: 10 }}>
          {t('coderQueue.dismiss', { defaultValue: 'Dismiss' })}
        </button>
        <button type="button" className="btn btn-primary btn-sm" onClick={run('start', () => onStart(item, cwd))} disabled={!!busy}
          style={{ fontSize: 10, gap: 4, display: 'inline-flex', alignItems: 'center' }}>
          {busy === 'start' ? <Loader size={11} className="chat-spin" /> : <Play size={11} />}
          {t('coderQueue.start', { defaultValue: 'Start' })}
        </button>
      </div>
    </div>
  )
}

// QueuedCoderStack shows the queued chats beside the dock, the newest
// first, two at a time with the rest behind "+N more".
export function QueuedCoderStack({ items, side, onStart, onDismiss }) {
  const { t } = useTranslation()
  const [all, setAll] = useState(false)
  if (!items.length) return null
  const shown = all ? items : items.slice(0, SHOWN)
  const more = items.length - shown.length
  return (
    <div data-testid="queued-coder-stack" aria-label={t('coderQueue.stackLabel', { defaultValue: 'Queued coder chats' })}
      style={{
        position: 'fixed', bottom: 44, zIndex: 9601, display: 'flex', flexDirection: 'column', gap: 8,
        maxHeight: 'calc(100vh - 120px)', overflowY: 'auto', ...(side === 'left' ? { left: 300 } : { right: 80 }),
      }}>
      {shown.map(it => <QueuedCoderBubble key={it.id} item={it} onStart={onStart} onDismiss={onDismiss} />)}
      {(more > 0 || all) && items.length > SHOWN && (
        <button type="button" className="btn btn-ghost btn-sm" onClick={() => setAll(a => !a)}
          style={{ alignSelf: 'flex-end', fontFamily: mono, fontSize: 10, background: 'var(--overlay)' }}>
          {all ? t('coderQueue.showFewer', { defaultValue: 'Show fewer' }) : t('coderQueue.more', { defaultValue: '+{{count}} more queued', count: more })}
        </button>
      )}
    </div>
  )
}
