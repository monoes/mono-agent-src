import { useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { useTranslation } from 'react-i18next'
import { X, FolderOpen } from 'lucide-react'
import { api } from '../../services/api.js'
import { ChatMarkdown } from './ChatMarkdown.jsx'

// A readable, rendered look at a markdown/text file a chat message names: a
// dialog over the app, not a navigation. The text comes from the Go side
// (confined to the chat's folder, size-capped) and renders through
// ChatMarkdown, the renderer chat messages use: no raw HTML, links obey the
// same URL rules, images are never fetched.
export function ChatFilePreview({ conversationId, token, label, onReveal, onClose }) {
  const { t } = useTranslation()
  const [state, setState] = useState({ loading: true })
  const closeRef = useRef(null)
  const dialogRef = useRef(null)

  useEffect(() => {
    let live = true
    Promise.resolve(api.readChatMarkdown(conversationId, token)).then(
      (text) => { if (live) setState({ text }) },
      (e) => { if (live) setState({ error: e?.message || String(e) }) },
    )
    return () => { live = false }
  }, [conversationId, token])

  useEffect(() => {
    closeRef.current?.focus()
    const onKey = (e) => {
      if (e.key === 'Escape') { e.stopPropagation(); onClose(); return }
      if (e.key !== 'Tab' || !dialogRef.current) return
      // keep Tab inside the dialog
      const items = [...dialogRef.current.querySelectorAll('button, a[href], [tabindex]:not([tabindex="-1"])')].filter((el) => !el.disabled)
      if (items.length === 0) { e.preventDefault(); return }
      const first = items[0]
      const last = items[items.length - 1]
      const at = document.activeElement
      if (!dialogRef.current.contains(at)) { e.preventDefault(); first.focus() }
      else if (e.shiftKey && at === first) { e.preventDefault(); last.focus() }
      else if (!e.shiftKey && at === last) { e.preventDefault(); first.focus() }
    }
    document.addEventListener('keydown', onKey, true)
    return () => document.removeEventListener('keydown', onKey, true)
  }, [onClose])

  const btn = { display: 'inline-flex', alignItems: 'center', gap: 4, background: 'transparent', border: '1px solid rgba(0,180,216,0.3)', borderRadius: 5, padding: '2px 8px', cursor: 'pointer', color: '#00b4d8', fontFamily: 'var(--font-mono)', fontSize: 10 }
  return createPortal(
    <div data-testid="chat-file-preview-backdrop" onMouseDown={(e) => { if (e.target === e.currentTarget) onClose() }}
      style={{ position: 'fixed', inset: 0, background: 'rgba(2,5,9,0.6)', zIndex: 1000, display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 24 }}>
      <div ref={dialogRef} role="dialog" aria-modal="true" aria-label={t('chatLinks.previewTitle', { name: label })} data-testid="chat-file-preview"
        style={{ width: 'min(720px, 100%)', maxHeight: '100%', display: 'flex', flexDirection: 'column', background: '#0a1018', border: '1px solid #1e3a4f', borderRadius: 8, boxShadow: '0 10px 40px rgba(0,0,0,0.5)' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '8px 10px', borderBottom: '1px solid #1e3a4f' }}>
          <span title={label} style={{ flex: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', fontFamily: 'var(--font-mono)', fontSize: 11, color: '#e2e8f0' }}>{label}</span>
          <button type="button" style={btn} onClick={onReveal} title={t('chatLinks.revealHint')}><FolderOpen size={11} />{t('chatLinks.reveal')}</button>
          <button type="button" ref={closeRef} style={btn} onClick={onClose} aria-label={t('chatLinks.close')}><X size={11} /></button>
        </div>
        <div style={{ overflow: 'auto', padding: 12 }}>
          {state.loading && <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)' }}>{t('chatLinks.loading')}</span>}
          {state.error && <span role="alert" style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: '#f87171' }}>{t('chatLinks.previewFailed', { message: state.error })}</span>}
          {state.text != null && <ChatMarkdown content={state.text} noPaths />}
        </div>
      </div>
    </div>,
    document.body,
  )
}
