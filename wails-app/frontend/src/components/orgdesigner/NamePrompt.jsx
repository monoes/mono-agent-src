// Small modal asking for one lowercase name (a section, a document type).
// The refusal reason from the backend is shown inline and the prompt stays
// open, so the user can fix the name without losing context.

import { useState } from 'react'
import { useTranslation } from 'react-i18next'

export default function NamePrompt({ title, hint, initial = '', submitLabel, error = '', onSubmit, onCancel }) {
  const { t } = useTranslation()
  const [value, setValue] = useState(initial)
  const [busy, setBusy] = useState(false)
  const submit = async (e) => {
    e.preventDefault()
    if (!value.trim() || busy) return
    setBusy(true)
    try { await onSubmit(value.trim()) } finally { setBusy(false) }
  }
  return (
    <div role="dialog" aria-label={title} data-testid="name-prompt" style={{ position: 'fixed', inset: 0, zIndex: 1100, display: 'flex', alignItems: 'center', justifyContent: 'center', background: 'rgba(0,0,0,0.45)' }}>
      <form onSubmit={submit} style={{ width: 340, background: 'var(--bg-card, #0b1220)', border: '1px solid var(--border)', borderRadius: 10, padding: 16, display: 'flex', flexDirection: 'column', gap: 8 }}>
        <strong style={{ fontFamily: 'var(--font-mono)', fontSize: 12 }}>{title}</strong>
        {hint && <span style={{ fontSize: 11, color: 'var(--text-muted)' }}>{hint}</span>}
        <input
          autoFocus
          aria-label={title}
          value={value}
          onChange={e => setValue(e.target.value)}
          onKeyDown={e => { if (e.key === 'Escape') onCancel() }}
          style={{ fontFamily: 'var(--font-mono)', fontSize: 12, padding: '6px 8px', background: 'var(--elevated)', border: '1px solid var(--border)', borderRadius: 6, color: 'var(--text)' }}
        />
        {error && <div role="alert" data-testid="name-prompt-error" style={{ fontSize: 11, color: 'var(--red)' }}>{error}</div>}
        <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
          <button type="button" onClick={onCancel} style={btn}>{t('ui6OrgsCanvas.cancel')}</button>
          <button type="submit" disabled={busy || !value.trim()} style={{ ...btn, borderColor: 'var(--teal, #00f5d4)', color: 'var(--teal, #00f5d4)' }}>{submitLabel ?? t('ui6OrgsCanvas.create')}</button>
        </div>
      </form>
    </div>
  )
}

const btn = { fontFamily: 'var(--font-mono)', fontSize: 11, padding: '4px 10px', background: 'transparent', border: '1px solid var(--border)', borderRadius: 6, color: 'var(--text-secondary)', cursor: 'pointer' }
