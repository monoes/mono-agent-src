import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { X } from 'lucide-react'
import { tasksApi } from '../services/tasks.js'
import { joinNotes, metaFacts, splitNotes } from '../lib/taskNotes.js'
import SessionTaskFields from './tasksessions/SessionTaskFields.jsx'

// The detail drawer of one card: edit title and notes, read its history,
// comment, and approve an Inbox card. readFirst: it opened so the person reads
// the card's notes before it leaves Inbox (plan ruling F6); moveTo: the column
// other than Ready it was moved to, offered once read. Every change goes
// through the board hook, which shells out to the CLI.
export default function TaskDrawer({ task, status, readFirst = false, moveTo = null, readOnly, onClose, onEdit, onComment, onApprove, onMoveTo, onArchive, archiving = false, archiveError = '', sessionEvents = [], focusArtifacts = false }) {
  const { t } = useTranslation()
  const [events, setEvents] = useState([])
  const [detail, setDetail] = useState(null) // the show document: a session task's artifacts come with it
  const [loadError, setLoadError] = useState('')
  const [title, setTitle] = useState(task.title)
  // The planner's closing [mission-meta] line is not editable text: it is shown as chips and written back as it was.
  const split = splitNotes(task.notes)
  const [notes, setNotes] = useState(split.text)
  const [text, setText] = useState('')

  // A remote change replaces a field only while that field is untouched, so
  // typing in progress is never overwritten; a different card resets both.
  const seen = useRef({ id: task.id, title: task.title, notes: split.text })
  useEffect(() => {
    const prev = seen.current
    const next = { id: task.id, title: task.title, notes: splitNotes(task.notes).text }
    seen.current = next
    if (prev.id !== next.id) { setTitle(next.title); setNotes(next.notes); return }
    setTitle(cur => (cur === prev.title ? next.title : cur))
    setNotes(cur => (cur === prev.notes ? next.notes : cur))
  }, [task.id, task.title, task.notes])
  useEffect(() => {
    let live = true
    tasksApi.show(task.id).then(r => {
      if (!live) return
      if (r?.error) setLoadError(r.error)
      else { setLoadError(''); setEvents(r?.events || []); setDetail(r) }
    })
    return () => { live = false }
  }, [task.id, task.updated_at, task.last_event?.at])

  const facts = metaFacts(split.meta)
  const dirty = title !== task.title || notes !== split.text
  const save = () => {
    const change = {}
    if (title !== task.title) change.title = title
    if (notes !== split.text) change.notes = joinNotes(notes, split.line)
    onEdit(task.id, change)
  }
  const send = () => {
    const v = text.trim()
    if (!v) return
    onComment(task.id, v)
    setText('')
  }

  return (
    <aside role="dialog" aria-label={task.title} className="tb-drawer">
      <button type="button" aria-label={t('tasks.close')} onClick={onClose} className="btn btn-ghost btn-icon btn-sm tb-drawer-close"><X size={16} /></button>
      {readFirst && status === 'inbox' && <div role="note" data-testid="read-first" className="tb-read-first">{moveTo ? t('tasks.drawer.readFirstMove') : t('tasks.drawer.readFirst')}</div>}
      <label className="tb-field">
        <div>{t('tasks.drawer.title')}</div>
        <input className="form-input" value={title} disabled={readOnly} onChange={e => setTitle(e.target.value)} />
      </label>
      <label className="tb-field">
        <div>{t('tasks.drawer.notes')}</div>
        <textarea className="form-textarea" value={notes} disabled={readOnly} rows={5} onChange={e => setNotes(e.target.value)} />
      </label>
      {(facts.files.length > 0 || facts.size || facts.risk) && (
        <dl className="ts-facts" data-testid="task-meta">
          {facts.files.length > 0 && <><dt>{t('tasks.drawer.metaFiles')}</dt><dd>{facts.files.map(f => <code key={f} className="ts-badge">{f}</code>)}</dd></>}
          {facts.size && <><dt>{t('tasks.drawer.metaSize')}</dt><dd><span className="ts-badge">{facts.size}</span></dd></>}
          {facts.risk && <><dt>{t('tasks.drawer.metaRisk')}</dt><dd><span className="ts-badge">{facts.risk}</span></dd></>}
        </dl>
      )}
      <div className="tb-drawer-actions">{!readOnly && <button className="btn btn-primary btn-sm" type="button" disabled={!dirty || !title.trim()} onClick={save}>{t('tasks.drawer.save')}</button>}
      {!readOnly && status === 'inbox' && onApprove && <button className="btn btn-secondary btn-sm" type="button" disabled={dirty} title={dirty ? t('tasks.drawer.saveFirst') : undefined} onClick={onApprove}>{t('tasks.approve')}</button>}
      {!readOnly && status === 'inbox' && moveTo && onMoveTo && <button className="btn btn-secondary btn-sm" type="button" disabled={dirty} title={dirty ? t('tasks.drawer.saveFirst') : undefined} onClick={onMoveTo}>{t('tasks.drawer.moveTo', { column: t(`tasks.column.${moveTo}`) })}</button>}
      {!readOnly && onArchive && <button className="btn btn-secondary btn-sm" type="button" disabled={archiving} onClick={onArchive}>{archiving ? t('tasks.archiving') : t('tasks.archive')}</button>}</div>
      {archiveError && <div role="alert">{t('tasks.archiveFailed')}: {archiveError}</div>}

      {task.session_id > 0 && <SessionTaskFields task={{ ...task, ...(detail?.task?.id === task.id ? detail.task : {}) }} artifacts={detail?.task?.id === task.id ? detail.artifacts || [] : []} focusArtifacts={focusArtifacts} sessionEvents={sessionEvents} />}

      <h3 className="tb-drawer-h">{t('tasks.drawer.history')}</h3>
      {loadError && <div role="alert">{t('tasks.drawer.loadError')}: {loadError}</div>}
      <ul className="tb-history">
        {events.map(e => (
          <li key={e.id}>
            <strong>{e.actor}</strong> {t(`tasks.drawer.kind.${e.kind}`, { defaultValue: e.kind })}{e.to_status ? ` → ${t(`tasks.column.${e.to_status}`, { defaultValue: e.to_status })}` : ''}
            {e.note && <div className="tb-note">{e.note}</div>}
          </li>
        ))}
      </ul>

      {!readOnly && (
        <div className="tb-comment">
          <textarea className="form-textarea" aria-label={t('tasks.drawer.comment')} placeholder={t('tasks.drawer.comment')} rows={2} value={text} onChange={e => setText(e.target.value)} />
          <button className="btn btn-secondary btn-sm" type="button" disabled={!text.trim()} onClick={send}>{t('tasks.drawer.send')}</button>
        </div>
      )}
    </aside>
  )
}
