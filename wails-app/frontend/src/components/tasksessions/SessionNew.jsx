import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { MAX_TITLE_RUNES, sessionTree, titleProblem, visibleRows } from '../../lib/taskSessionModel.js'

const MAX_DEPTH = 4 // a session may sit this deep; the CLI has the last word

// The New session form: a title (required), a goal, and a parent from the
// listed sessions. A refusal (from here or from the CLI) stays under the
// fields with a Retry; nothing is cut or sent twice.
export default function SessionNew({ sessions, defaultParent = 0, onCreate, onClose }) {
  const { t } = useTranslation()
  const [title, setTitle] = useState('')
  const [goal, setGoal] = useState('')
  const [parent, setParent] = useState(defaultParent || 0)
  const [error, setError] = useState('')
  const [refused, setRefused] = useState(false) // the CLI refused (Retry makes sense); else our own check
  const [busy, setBusy] = useState(false)
  const titleRef = useRef(null)
  useEffect(() => { titleRef.current?.focus() }, [])
  const options = visibleRows(sessionTree(sessions), new Set())

  const submit = async (e) => {
    e?.preventDefault()
    if (busy) return
    const problem = titleProblem(title)
    if (problem) { setRefused(false); setError(t(`tasks.session.titleProblem.${problem}`, { max: MAX_TITLE_RUNES })); titleRef.current?.focus(); return }
    setBusy(true)
    const res = await onCreate({ title: title.trim(), goal: goal.trim(), parentId: Number(parent) || 0 })
    setBusy(false)
    if (res?.error) { setRefused(true); setError(res.error) }
    else onClose(res)
  }
  return (
    <form
      className="ts-new" role="dialog" noValidate aria-label={t('tasks.session.new')} onSubmit={submit}
      onKeyDown={e => { if (e.key === 'Escape') { e.stopPropagation(); onClose(null) } }}
    >
      <label className="ts-field">
        <span>{t('tasks.session.titleLabel')}</span>
        <input ref={titleRef} className="form-input" value={title} required aria-required="true" onChange={e => { setTitle(e.target.value); setError('') }} />
      </label>
      <label className="ts-field">
        <span>{t('tasks.session.goalLabel')}</span>
        <textarea className="form-textarea" rows={3} value={goal} onChange={e => setGoal(e.target.value)} />
      </label>
      <label className="ts-field">
        <span>{t('tasks.session.parentLabel')}</span>
        <select className="form-select" value={parent} onChange={e => setParent(Number(e.target.value))}>
          <option value={0}>{t('tasks.session.noParent')}</option>
          {options.map(r => (
            <option key={r.session.id} value={r.session.id} disabled={r.depth >= MAX_DEPTH}>
              {`${'– '.repeat(r.depth - 1)}${r.session.title}`}
            </option>
          ))}
        </select>
      </label>
      {error && (
        <p className="ts-form-error" role="alert">
          {error}
          {refused && <button type="button" className="btn btn-ghost btn-sm" onClick={submit} disabled={busy}>{t('tasks.session.retry')}</button>}
        </p>
      )}
      <div className="ts-form-actions">
        <button type="button" className="btn btn-ghost btn-sm" onClick={() => onClose(null)}>{t('tasks.session.cancel')}</button>
        <button type="submit" className="btn btn-primary btn-sm" disabled={busy}>{t('tasks.session.create')}</button>
      </div>
    </form>
  )
}
