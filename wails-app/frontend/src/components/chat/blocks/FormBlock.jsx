import { useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useBlockEnv } from './context.js'
import { useAnswer } from './answers.js'
import { formErrors, formMessage } from './formSchema.js'
import { panel, heading, note, primaryButton, COLOR } from './ui.js'

const input = (bad) => ({
  width: '100%', boxSizing: 'border-box', background: '#020509', color: COLOR.text, fontFamily: 'var(--font-mono)', fontSize: 11,
  border: `1px solid ${bad ? COLOR.error : COLOR.border}`, borderRadius: 4, padding: '4px 6px',
})

// form: a small typed form. Submit sends the values as a user message through
// the chat's onSend and locks the form to the submitted values (remembered
// locally, see answers.js). Without onSend it renders disabled with a note.
export function FormBlock({ data, blockKey }) {
  const { t } = useTranslation()
  const uid = useId()
  const { onSend } = useBlockEnv()
  const [answer, remember] = useAnswer(blockKey)
  const [values, setValues] = useState(() => Object.fromEntries(data.fields.map(f => [f.id, f.default])))
  const [tried, setTried] = useState(false)
  const [busy, setBusy] = useState(false)
  const locked = !!answer
  const shown = locked ? (answer.values || values) : values
  const disabled = locked || !onSend || busy
  const errors = formErrors(data.fields, shown)

  const submit = async (e) => {
    e.preventDefault()
    setTried(true)
    if (disabled || Object.keys(errors).length) return
    const text = formMessage(data, values)
    setBusy(true)
    try {
      if (await onSend(text)) remember({ text, values })
    } finally { setBusy(false) }
  }
  const set = (id, v) => setValues(s => ({ ...s, [id]: v }))

  return (
    <form onSubmit={submit} noValidate aria-label={data.title || t('chatBlocks.form.label')} style={panel}>
      {data.title && <p style={heading}>{data.title}</p>}
      <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        {data.fields.map((f) => {
          const id = `${uid}-${f.id}`
          const err = tried && !locked ? errors[f.id] : undefined
          const common = { id, disabled, required: f.required, 'aria-invalid': err ? true : undefined, 'aria-describedby': err ? `${id}-err` : undefined }
          return (
            <div key={f.id}>
              {f.type === 'checkbox' ? (
                <label style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                  <input type="checkbox" {...common} checked={!!shown[f.id]} onChange={e => set(f.id, e.target.checked)} />
                  {f.label}{f.required && ' *'}
                </label>
              ) : (
                <>
                  <label htmlFor={id} style={{ display: 'block', fontSize: 10, color: COLOR.muted, marginBottom: 2 }}>{f.label}{f.required && ' *'}</label>
                  {f.type === 'select' ? (
                    <select {...common} value={shown[f.id]} onChange={e => set(f.id, e.target.value)} style={input(err)}>
                      <option value="">{f.placeholder || '-'}</option>
                      {f.options.map(o => <option key={o.value} value={o.value}>{o.label}</option>)}
                    </select>
                  ) : (
                    <input {...common} type={f.type === 'number' ? 'number' : 'text'} min={f.min} max={f.max} step={f.type === 'number' ? 'any' : undefined}
                      maxLength={f.type === 'text' ? 200 : undefined} placeholder={f.placeholder} value={shown[f.id]} onChange={e => set(f.id, e.target.value)} style={input(err)} />
                  )}
                </>
              )}
              {err && <div id={`${id}-err`} role="alert" style={{ fontSize: 10, color: COLOR.error, marginTop: 2 }}>{t(`chatBlocks.form.error.${err}`)}</div>}
            </div>
          )
        })}
      </div>
      {!locked && <button type="submit" disabled={disabled} style={primaryButton(disabled)}>{data.submitLabel || t('chatBlocks.form.submit')}</button>}
      {locked && <div style={note}>{t('chatBlocks.answered')}</div>}
      {!locked && !onSend && <div style={note}>{t('chatBlocks.cannotSend')}</div>}
    </form>
  )
}
