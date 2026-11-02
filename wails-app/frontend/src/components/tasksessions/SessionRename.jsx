import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { MAX_TITLE_RUNES, titleProblem } from '../../lib/taskSessionModel.js'

// Inline rename of the chosen session: Enter saves, Escape cancels. A title
// that is empty or too long is refused with a message (never cut); a refusal
// from the CLI stays beside the field with a Retry.
export default function SessionRename({ session, onRename, onDone }) {
  const { t } = useTranslation()
  const [title, setTitle] = useState(session.title)
  const [error, setError] = useState('')
  const [refused, setRefused] = useState(false) // the CLI refused (Retry makes sense); else our own check
  const [busy, setBusy] = useState(false)
  const ref = useRef(null)
  useEffect(() => { ref.current?.focus(); ref.current?.select() }, [])

  const save = async () => {
    if (busy) return
    const problem = titleProblem(title)
    if (problem) { setRefused(false); setError(t(`tasks.session.titleProblem.${problem}`, { max: MAX_TITLE_RUNES })); return }
    if (title.trim() === session.title) { onDone(); return }
    setBusy(true)
    const res = await onRename(session.id, title.trim())
    setBusy(false)
    if (res?.error) { setRefused(true); setError(res.error) }
    else onDone()
  }
  return (
    <span className="ts-rename">
      <input
        ref={ref} className="form-input" value={title} aria-label={t('tasks.session.renameLabel')} aria-invalid={!!error}
        onChange={e => { setTitle(e.target.value); setError('') }}
        onKeyDown={e => {
          if (e.key === 'Enter') { e.preventDefault(); save() }
          else if (e.key === 'Escape') { e.preventDefault(); onDone() }
        }}
      />
      {error && (
        <span className="ts-form-error" role="alert">
          {error}
          {refused && <button type="button" className="btn btn-ghost btn-sm" onClick={save} disabled={busy}>{t('tasks.session.retry')}</button>}
        </span>
      )}
    </span>
  )
}
