import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

const titleStyle = { fontFamily: 'var(--font-display)', fontSize: 13, fontWeight: 600, color: 'var(--text)' }
const MAX = 80 // the CLI cuts a name to this

// ChatTitle is the name at the top of a chat. Double-click it (or press Enter or F2 on it) to
// rename: Enter or leaving the box saves, Esc puts the old name back, and an empty name goes back
// to the derived one. onSave(next) resolves once it is stored and rejects with the reason.
export function ChatTitle({ title, canEdit, onSave }) {
  const { t } = useTranslation()
  const [editing, setEditing] = useState(false)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const settled = useRef(false) // a commit or a cancel already ran for this edit (Enter, then the box goes and blurs)
  const input = useRef(null)
  useEffect(() => { if (editing) { input.current?.focus(); input.current?.select() } }, [editing])

  const begin = () => { if (!canEdit) return; settled.current = false; setError(''); setEditing(true) }
  const cancel = () => { settled.current = true; setError(''); setEditing(false) }
  const commit = async () => {
    if (settled.current) return
    settled.current = true
    const next = input.current.value.trim()
    if (next === title) { setEditing(false); return }
    setBusy(true)
    try {
      await onSave(next)
      setEditing(false)
    } catch (err) {
      settled.current = false
      setError(t('bubbles.renameFailed', { error: String(err?.message || err) }))
    } finally {
      setBusy(false)
    }
  }

  if (editing) {
    return (
      <span style={{ display: 'inline-flex', flexDirection: 'column', minWidth: 0, maxWidth: '100%' }}>
        <input ref={input} data-title-editor defaultValue={title} maxLength={MAX} disabled={busy}
          aria-label={t('bubbles.renameLabel')} aria-invalid={error ? true : undefined} className="input-compact"
          placeholder={t('bubbles.renamePlaceholder')}
          style={{ ...titleStyle, width: 'min(360px, 100%)', padding: '1px 6px' }}
          onKeyDown={e => {
            if (e.key === 'Enter') { e.preventDefault(); commit() }
            else if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); cancel() }
          }}
          onBlur={commit} />
        {error && <span role="alert" style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--red)' }}>{error}</span>}
      </span>
    )
  }
  return (
    <span data-testid="chat-title" style={{ ...titleStyle, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', maxWidth: '100%', cursor: canEdit ? 'text' : undefined }}
      {...(canEdit ? {
        role: 'button', tabIndex: 0, title: t('bubbles.renameHint'), 'aria-label': t('bubbles.renameAria', { name: title }),
        onDoubleClick: begin,
        onKeyDown: e => { if (e.key === 'F2' || (e.key === 'Enter' && e.target === e.currentTarget)) { e.preventDefault(); begin() } },
      } : {})}>{title}</span>
  )
}
