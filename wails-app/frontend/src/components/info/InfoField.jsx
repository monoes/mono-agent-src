import { useEffect, useRef, useState, useId } from 'react'
import { useTranslation } from 'react-i18next'
import { Eye, EyeOff } from 'lucide-react'
import * as WailsApp from '../../wailsjs/go/main/App'
import { errorText, hasValue } from './infoModel.js'
import InfoPhotoSlot from './InfoPhotoSlot.jsx'

const REVEAL_MS = 60000 // a shown secret hides itself again after a minute
const inputType = { date: 'date', email: 'email', phone: 'tel' }

// One field, rendered by its catalog kind. Autosaves on blur (change for a
// select) through onSave(key, value), which rejects with the CLI's message.
export default function InfoField({ field, entry, onSave, onChanged }) {
  if (field.kind === 'photo') return <InfoPhotoSlot field={field} entry={entry} onChanged={onChanged} />
  return <InfoValueField field={field} entry={entry} onSave={onSave} />
}

function InfoValueField({ field, entry, onSave }) {
  const { t } = useTranslation()
  const id = useId()
  const stored = entry?.value ?? ''
  const masked = !!entry?.masked
  const [draft, setDraft] = useState(stored)
  const [status, setStatus] = useState('idle') // idle | saving | saved | error
  const [message, setMessage] = useState('')
  const [changing, setChanging] = useState(false)
  const [revealed, setRevealed] = useState(null)
  const [revealError, setRevealError] = useState('')
  const timers = useRef({})
  const textarea = field.kind === 'textarea'

  useEffect(() => { setDraft(stored) }, [stored])
  useEffect(() => () => { Object.values(timers.current).forEach(clearTimeout) }, [])
  // Leaving the masked state (a new value was saved) drops any revealed copy.
  useEffect(() => { if (!masked) { setRevealed(null); setChanging(false) } }, [masked])

  const commit = async (value) => {
    if (value === stored) return
    setStatus('saving'); setMessage('')
    try {
      await onSave(field.key, value)
      setStatus('saved'); setChanging(false)
      clearTimeout(timers.current.saved)
      timers.current.saved = setTimeout(() => setStatus('idle'), 2500)
    } catch (e) {
      setStatus('error'); setMessage(errorText(e))
    }
  }

  const reveal = async () => {
    setRevealError('')
    try {
      const v = await WailsApp.RevealProfileInfo(field.key)
      setRevealed(v)
      clearTimeout(timers.current.reveal)
      timers.current.reveal = setTimeout(() => setRevealed(null), REVEAL_MS)
    } catch (e) {
      setRevealError(t('info.revealFailed', { message: errorText(e) }))
    }
  }

  const hide = () => {
    clearTimeout(timers.current.reveal)
    setRevealed(null)
  }

  const aiBadge = entry?.source === 'ai' && hasValue(entry)
  const aiTitle = entry?.origin ? t('info.aiFrom', { origin: entry.origin }) : t('info.aiFromUnknown')
  const showSecret = masked && !changing
  const common = {
    id, className: `info-input${status === 'error' ? ' is-error' : ''}`,
    'aria-invalid': status === 'error' || undefined,
    'aria-describedby': `${id}-msg`,
    autoComplete: 'off',
  }

  let control
  if (showSecret) {
    control = (
      <div className={`info-secret${revealed === null ? '' : ' is-shown'}`}>
        <input id={id} className={`info-input info-secret-value${revealed === null ? ' is-blurred' : ''}`} readOnly value={revealed ?? stored} aria-describedby={`${id}-msg`} />
        <button type="button" className="info-btn" onClick={() => { setDraft(''); setChanging(true) }} aria-label={`${t('info.change')}: ${field.label}`}>{t('info.change')}</button>
        <button type="button" className="info-btn info-eye" onClick={revealed === null ? reveal : hide} aria-pressed={revealed !== null}
          aria-label={`${t(revealed === null ? 'info.reveal' : 'info.hide')}: ${field.label}`} title={t(revealed === null ? 'info.reveal' : 'info.hide')}>
          {revealed === null ? <Eye size={14} aria-hidden="true" /> : <EyeOff size={14} aria-hidden="true" />}
        </button>
      </div>
    )
  } else if (changing) {
    control = (
      <div className="info-secret">
        <input {...common} type="text" value={draft} placeholder={t('info.keepHint')} autoFocus
          onChange={e => setDraft(e.target.value)}
          onBlur={() => (draft === '' ? setChanging(false) : commit(draft))}
          onKeyDown={e => { if (e.key === 'Escape') setChanging(false) }} />
      </div>
    )
  } else if (field.kind === 'select') {
    control = (
      <select {...common} className={`form-select${status === 'error' ? ' is-error' : ''}`} value={draft} onChange={e => { setDraft(e.target.value); commit(e.target.value) }}>
        <option value="" />
        {(field.options || []).map(o => <option key={o} value={o}>{o}</option>)}
        {draft && !(field.options || []).includes(draft) && <option value={draft}>{draft}</option>}
      </select>
    )
  } else if (textarea) {
    control = <textarea {...common} value={draft} onChange={e => setDraft(e.target.value)} onBlur={() => commit(draft)} />
  } else {
    control = (
      <input {...common} type={inputType[field.kind] || 'text'} value={draft}
        autoComplete={field.autocomplete || 'off'}
        onChange={e => setDraft(e.target.value)} onBlur={() => commit(draft)}
        onKeyDown={e => { if (e.key === 'Enter') e.currentTarget.blur() }} />
    )
  }

  const statusText = status === 'saving' ? t('info.saving') : status === 'saved' ? t('info.saved') : ''
  return (
    <div className={`info-field${textarea ? ' is-wide' : ''}`}>
      <div className="info-label-row">
        <label className="info-label" htmlFor={id}>{field.label}</label>
        {aiBadge && <span className="info-badge" title={aiTitle} aria-label={aiTitle}>{t('info.aiBadge')}</span>}
        <span className="info-status" role="status">{statusText}</span>
      </div>
      {control}
      <div className={message || revealError ? 'info-error-text' : 'info-error-text is-hint'} id={`${id}-msg`} role={message || revealError ? 'alert' : undefined}>
        {message || revealError || (revealed !== null ? t('info.revealed') : '')}
      </div>
    </div>
  )
}
