import { useState, useEffect, useCallback, useRef } from 'react'

// Promise-based confirm to replace blocking native window.confirm(), which looks
// foreign in a styled Wails app and freezes the webview. Usage:
//   if (!(await confirm('Delete this?'))) return
// With opts.checkbox = { label, warning } the dialog also shows an unchecked
// checkbox, and confirming resolves { checked } (truthy) instead of true.
const bus = new EventTarget()
let pendingResolve = null

export function confirm(message, opts = {}) {
  return new Promise((resolve) => {
    pendingResolve = resolve
    bus.dispatchEvent(new CustomEvent('confirm:open', { detail: { message, ...opts } }))
  })
}

function settle(value) {
  if (pendingResolve) {
    pendingResolve(value)
    pendingResolve = null
  }
}

// ConfirmHost renders the dialog; mount once near the app root.
export default function ConfirmHost() {
  const [req, setReq] = useState(null)
  const [checked, setChecked] = useState(false)
  const dialogRef = useRef(null)
  const openerRef = useRef(null)

  const close = useCallback((value) => {
    settle(value === true && req?.checkbox ? { checked } : value)
    setReq(null)
  }, [req, checked])

  useEffect(() => {
    const handler = (e) => {
      // Remember what had focus, so closing the dialog hands it back.
      if (!openerRef.current) openerRef.current = document.activeElement
      setChecked(false); setReq(e.detail)
    }
    bus.addEventListener('confirm:open', handler)
    return () => bus.removeEventListener('confirm:open', handler)
  }, [])

  // On close, focus returns to the element that had it when the dialog opened
  // (if it is still on the page), not to <body>.
  useEffect(() => {
    if (req) return
    const opener = openerRef.current
    openerRef.current = null
    if (opener && opener !== document.body && document.contains(opener)) opener.focus?.()
  }, [req])

  // Trap Tab focus within the dialog so keyboard users can't tab out to the
  // obscured page behind it.
  useEffect(() => {
    if (!req) return
    const onTab = (e) => {
      if (e.key !== 'Tab' || !dialogRef.current) return
      const focusable = dialogRef.current.querySelectorAll('button, [href], input, [tabindex]:not([tabindex="-1"])')
      if (!focusable.length) return
      const first = focusable[0]
      const last = focusable[focusable.length - 1]
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault(); last.focus()
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault(); first.focus()
      }
    }
    document.addEventListener('keydown', onTab)
    return () => document.removeEventListener('keydown', onTab)
  }, [req])

  useEffect(() => {
    if (!req) return
    const onKey = (e) => {
      if (e.key === 'Escape') close(false)
      // A focused button answers Enter itself (Cancel with focus means
      // cancel). Only Enter with no button focused confirms; the confirm
      // button has focus when the dialog opens, so Enter still confirms by
      // default.
      if (e.key === 'Enter' && !(document.activeElement instanceof HTMLButtonElement)) close(true)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [req, close])

  if (!req) return null

  const danger = req.danger !== false // default to danger styling
  return (
    <div
      className="modal-overlay"
      onClick={(e) => e.target === e.currentTarget && close(false)}
      style={{
        position: 'fixed', inset: 0, zIndex: 10001,
        display: 'flex', alignItems: 'center', justifyContent: 'center',
        background: 'rgba(0,0,0,0.55)',
      }}
    >
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-label={req.title || 'Confirm'}
        style={{
          background: 'var(--surface, #0d1520)',
          border: '1px solid var(--border, rgba(255,255,255,0.1))',
          borderRadius: 10, padding: '20px 22px', maxWidth: 400, width: '90%',
          boxShadow: '0 12px 40px rgba(0,0,0,0.6)', fontFamily: 'var(--font-mono)',
        }}
      >
        {req.title && (
          <div style={{ fontSize: 14, fontWeight: 600, color: 'var(--text)', marginBottom: 8 }}>{req.title}</div>
        )}
        <div style={{ fontSize: 12.5, color: 'var(--text-secondary)', lineHeight: 1.5, marginBottom: 18 }}>
          {req.message}
        </div>
        {req.checkbox && (
          <div style={{ marginBottom: 18 }}>
            <label style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 12.5, color: 'var(--text)' }}>
              <input type="checkbox" checked={checked} onChange={(e) => setChecked(e.target.checked)} />
              {req.checkbox.label}
            </label>
            {req.checkbox.warning && (
              <div style={{ fontSize: 11.5, color: '#ef4444', marginTop: 6, lineHeight: 1.4 }}>{req.checkbox.warning}</div>
            )}
          </div>
        )}
        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 10 }}>
          <button
            onClick={() => close(false)}
            style={{
              padding: '7px 14px', borderRadius: 6, cursor: 'pointer', fontSize: 12,
              background: 'transparent', border: '1px solid var(--border, rgba(255,255,255,0.15))',
              color: 'var(--text-secondary)', fontFamily: 'var(--font-mono)',
            }}
          >
            {req.cancelLabel || 'Cancel'}
          </button>
          <button
            autoFocus
            onClick={() => close(true)}
            style={{
              padding: '7px 14px', borderRadius: 6, cursor: 'pointer', fontSize: 12, border: 'none',
              background: danger ? '#ef4444' : '#00b4d8', color: '#fff', fontFamily: 'var(--font-mono)',
            }}
          >
            {req.confirmLabel || 'Confirm'}
          </button>
        </div>
      </div>
    </div>
  )
}
