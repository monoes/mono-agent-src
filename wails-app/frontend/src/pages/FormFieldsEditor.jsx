// FormFieldsEditor is the inspector's editor for a node's formFields
// (trigger.form, the Wait node's form, form pages): a card per field, a
// live preview of the served form, and the raw JSON editor behind a
// toggle. The rules it applies live in formFieldsModel.js.
//
// Edits are saved only when every field is valid, so the stored value is
// never one the engine would reject; the draft stays on screen with the
// problems marked.
import { useState, useRef, useEffect } from 'react'
import { ChevronUp, ChevronDown, Trash2, Plus } from 'lucide-react'
import {
  FORM_FIELD_TYPES, OPTION_TYPES, parseFormFields, serializeFormFields, validateFormFields,
  newField, moveItem,
} from './formFieldsModel.js'
import { collectionText, jsonTextError } from './nodeInspectorRules.js'

const mono = { fontFamily: 'var(--font-mono)' }
const small = { ...mono, fontSize: 9, color: 'var(--text-muted)', letterSpacing: 1.2, textTransform: 'uppercase' }
const errorStyle = { ...mono, fontSize: 9, color: '#f87171', marginTop: 4 }
const linkButton = {
  background: 'transparent', border: 'none', cursor: 'pointer',
  ...mono, fontSize: 9, color: '#00b4d8', padding: '4px 0',
}
const iconButton = {
  background: 'transparent', border: 'none', cursor: 'pointer', color: 'var(--text-muted)',
  display: 'inline-flex', alignItems: 'center', justifyContent: 'center', padding: 4, borderRadius: 4,
}
const card = {
  border: '1px solid rgba(0,180,216,0.15)', borderRadius: 6, padding: 8, marginBottom: 8,
  background: 'rgba(0,180,216,0.03)',
}

function IconButton({ label, onClick, disabled, children }) {
  return (
    <button type="button" aria-label={label} title={label} onClick={onClick} disabled={disabled}
      style={{ ...iconButton, opacity: disabled ? 0.35 : 1, cursor: disabled ? 'default' : 'pointer' }}>
      {children}
    </button>
  )
}

export default function FormFieldsEditor({ field, value, config, onCommit, inputStyle }) {
  const [state, setState] = useState(() => {
    const parsed = parseFormFields(value)
    return parsed.error
      ? { mode: 'json', fields: [], text: collectionText(value), note: parsed.error }
      : { mode: 'visual', fields: parsed.fields, text: '', note: '' }
  })
  const [showPreview, setShowPreview] = useState(true)
  const focusId = useRef(null)
  const labelRefs = useRef({})
  useEffect(() => {
    if (focusId.current && labelRefs.current[focusId.current]) {
      labelRefs.current[focusId.current].focus()
      focusId.current = null
    }
  })

  const { mode, fields, text, note } = state
  const errors = validateFormFields(fields)

  const update = next => {
    setState(s => ({ ...s, fields: next }))
    if (Object.keys(validateFormFields(next)).length === 0) onCommit(serializeFormFields(next))
  }
  const patch = (id, changes) => update(fields.map(f => (f.id === id ? { ...f, ...changes } : f)))

  const toJSON = () => setState(s => ({
    ...s, mode: 'json', note: '', text: JSON.stringify(serializeFormFields(s.fields), null, 2),
  }))
  const toVisual = () => {
    const parsed = parseFormFields(text)
    if (parsed.error) { setState(s => ({ ...s, note: parsed.error })); return }
    setState(s => ({ ...s, mode: 'visual', fields: parsed.fields, note: '' }))
  }

  const header = (
    <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 6 }}>
      <span style={{ ...small, flex: 1 }}>
        {mode === 'json' ? field.label : String(field.label || '').replace(/\s*\(JSON\)$/i, '')}{field.required ? ' *' : ''}
      </span>
      {mode === 'visual' && (
        <button type="button" style={linkButton} aria-pressed={showPreview} onClick={() => setShowPreview(p => !p)}>
          {showPreview ? 'Hide preview' : 'Preview'}
        </button>
      )}
      <button type="button" style={linkButton} aria-pressed={mode === 'json'} onClick={mode === 'json' ? toVisual : toJSON}>
        {mode === 'json' ? 'Visual editor' : 'Raw JSON'}
      </button>
    </div>
  )

  if (mode === 'json') {
    return (
      <div className="config-field" style={{ marginBottom: 10 }}>
        {header}
        <textarea
          className="field-code"
          aria-label={`${field.label} JSON`}
          rows={field.rows || 10}
          value={text}
          onChange={e => { setState(s => ({ ...s, text: e.target.value, note: '' })); onCommit(e.target.value) }}
          placeholder={field.placeholder || ''}
          spellCheck={false}
          autoComplete="off"
          style={{ ...inputStyle, fontFamily: 'var(--font-mono)', fontSize: 11 }}
        />
        {(note || jsonTextError(text)) && <div role="alert" style={errorStyle}>{note || jsonTextError(text)}</div>}
        {field.help && <p className="field-help">{field.help}</p>}
      </div>
    )
  }

  return (
    <div className="config-field" style={{ marginBottom: 10 }}>
      {header}
      {fields.length === 0 && (
        <div style={{ ...mono, fontSize: 10, color: 'var(--text-muted)', marginBottom: 6 }}>
          No fields yet. A form needs at least one.
        </div>
      )}
      <ol aria-label="Form fields" style={{ listStyle: 'none', margin: 0, padding: 0 }}>
        {fields.map((f, i) => (
          <FieldCard
            key={f.id}
            f={f}
            index={i}
            count={fields.length}
            error={errors[f.id]}
            inputStyle={inputStyle}
            labelRef={el => { labelRefs.current[f.id] = el }}
            onPatch={changes => patch(f.id, changes)}
            onMove={delta => update(moveItem(fields, i, delta))}
            onRemove={() => update(fields.filter(x => x.id !== f.id))}
          />
        ))}
      </ol>
      <button
        type="button"
        style={{ ...linkButton, display: 'inline-flex', alignItems: 'center', gap: 4 }}
        onClick={() => { const nf = newField(); focusId.current = nf.id; update([...fields, nf]) }}
      >
        <Plus size={10} /> Add field
      </button>
      {Object.keys(errors).length > 0 && (
        <div style={errorStyle}>Not saved until the marked fields are fixed.</div>
      )}
      {showPreview && <FormPreview fields={fields} config={config} />}
    </div>
  )
}

function FieldCard({ f, index, count, error, inputStyle, labelRef, onPatch, onMove, onRemove }) {
  const n = index + 1
  const errorId = `form-field-${f.id}-error`
  const types = FORM_FIELD_TYPES.includes(f.fieldType) ? FORM_FIELD_TYPES : [...FORM_FIELD_TYPES, f.fieldType]
  const takesOptions = OPTION_TYPES.has(f.fieldType)
  const takesPlaceholder = !takesOptions
  const options = f.options || []
  const setOptions = next => onPatch({ options: next })
  return (
    <li style={{ ...card, ...(error ? { borderColor: 'rgba(248,113,113,0.6)' } : null) }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 2, marginBottom: 6 }}>
        <span style={{ ...small, flex: 1 }}>Field {n}</span>
        <IconButton label={`Move field ${n} up`} disabled={index === 0} onClick={() => onMove(-1)}><ChevronUp size={12} /></IconButton>
        <IconButton label={`Move field ${n} down`} disabled={index === count - 1} onClick={() => onMove(1)}><ChevronDown size={12} /></IconButton>
        <IconButton label={`Remove field ${n}`} onClick={onRemove}><Trash2 size={12} /></IconButton>
      </div>
      <input
        ref={labelRef}
        aria-label={`Field ${n} label`}
        aria-invalid={error ? true : undefined}
        aria-describedby={error ? errorId : undefined}
        placeholder="Label, e.g. Email"
        value={f.fieldLabel}
        onChange={e => onPatch({ fieldLabel: e.target.value })}
        style={{ ...inputStyle, marginBottom: 6 }}
      />
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 6 }}>
        <select
          aria-label={`Field ${n} type`}
          className="select-compact"
          value={f.fieldType}
          onChange={e => onPatch({ fieldType: e.target.value })}
          style={{ flex: 1, fontSize: 11 }}
        >
          {types.map(t => <option key={t} value={t}>{t}</option>)}
        </select>
        <label style={{ ...mono, fontSize: 10, color: 'var(--text-muted)', display: 'inline-flex', alignItems: 'center', gap: 4, whiteSpace: 'nowrap' }}>
          <input
            type="checkbox"
            checked={!!f.requiredField}
            onChange={e => onPatch({ requiredField: e.target.checked })}
            style={{ accentColor: '#00b4d8', width: 13, height: 13 }}
          />
          Required
        </label>
      </div>
      {takesPlaceholder && (
        <input
          aria-label={`Field ${n} placeholder`}
          placeholder="Placeholder (optional)"
          value={f.placeholder}
          onChange={e => onPatch({ placeholder: e.target.value })}
          style={inputStyle}
        />
      )}
      {takesOptions && (
        <div role="group" aria-label={`Field ${n} options`}>
          <div style={{ ...small, margin: '2px 0 4px' }}>
            Options{f.fieldType === 'checkbox' ? ' (none: a single box)' : ''}
          </div>
          {options.map((o, oi) => (
            <div key={oi} style={{ display: 'flex', alignItems: 'center', gap: 2, marginBottom: 4 }}>
              <input
                aria-label={`Field ${n} option ${oi + 1}`}
                value={o.option}
                onChange={e => setOptions(options.map((x, xi) => (xi === oi ? { ...x, option: e.target.value } : x)))}
                style={{ ...inputStyle, flex: 1 }}
              />
              <IconButton label={`Move option ${oi + 1} up`} disabled={oi === 0} onClick={() => setOptions(moveItem(options, oi, -1))}><ChevronUp size={11} /></IconButton>
              <IconButton label={`Move option ${oi + 1} down`} disabled={oi === options.length - 1} onClick={() => setOptions(moveItem(options, oi, 1))}><ChevronDown size={11} /></IconButton>
              <IconButton label={`Remove option ${oi + 1}`} onClick={() => setOptions(options.filter((_, xi) => xi !== oi))}><Trash2 size={11} /></IconButton>
            </div>
          ))}
          <button type="button" style={{ ...linkButton, display: 'inline-flex', alignItems: 'center', gap: 4 }}
            onClick={() => setOptions([...options, { option: '' }])}>
            <Plus size={10} /> Add option
          </button>
        </div>
      )}
      {error && <div id={errorId} role="alert" style={errorStyle}>{error}</div>}
    </li>
  )
}

// FormPreview draws the form roughly as /form/<path> serves it (the served
// page's dark scheme): title, description, each field, the button.
const pv = {
  box: { marginTop: 10, borderRadius: 8, background: '#232321', color: '#eee', padding: 14, font: '13px/1.5 system-ui, sans-serif' },
  label: { display: 'block', fontWeight: 600, marginTop: 12 },
  input: {
    display: 'block', width: '100%', boxSizing: 'border-box', marginTop: 4, padding: '6px 8px', font: 'inherit',
    border: '1px solid #8a8a85', borderRadius: 4, background: '#1b1b1a', color: '#eee',
  },
  button: { marginTop: 16, padding: '7px 16px', font: 'inherit', border: 0, borderRadius: 4, background: '#1d4ed8', color: '#fff' },
}

// The preview's select keeps the app's select look (index.css): layout only.
const previewSelect = { display: 'block', width: '100%', marginTop: 4, fontSize: 13 }

export function FormPreview({ fields, config }) {
  const title = String(config?.formTitle ?? '').trim()
  const description = String(config?.formDescription ?? '').trim()
  const button = String(config?.buttonLabel ?? '').trim() || 'Submit'
  return (
    <section aria-label="Form preview" style={{ marginTop: 6 }}>
      <div style={small}>Preview</div>
      <div style={pv.box} inert>
        {title && <h1 style={{ fontSize: 18, margin: '0 0 6px' }}>{title}</h1>}
        {description && <p style={{ margin: '0 0 4px', whiteSpace: 'pre-wrap' }}>{description}</p>}
        {fields.map(f => <PreviewField key={f.id} f={f} />)}
        <button type="button" tabIndex={-1} style={pv.button}>{button}</button>
      </div>
    </section>
  )
}

function PreviewField({ f }) {
  const label = String(f.fieldLabel ?? '').trim() || 'Untitled field'
  const star = f.requiredField ? <span aria-hidden="true"> *</span> : null
  const options = (f.options || []).map(o => String(o.option ?? '').trim()).filter(Boolean)
  if (f.fieldType === 'checkbox' && options.length) {
    return (
      <fieldset style={{ border: 0, padding: 0, margin: 0 }}>
        <legend style={pv.label}>{label}{star}</legend>
        {options.map(o => <label key={o} style={{ display: 'block', marginTop: 4 }}><input type="checkbox" tabIndex={-1} /> {o}</label>)}
      </fieldset>
    )
  }
  if (f.fieldType === 'checkbox') {
    return <label style={pv.label}><input type="checkbox" tabIndex={-1} /> {label}{star}</label>
  }
  let control
  if (f.fieldType === 'textarea') control = <textarea rows={3} tabIndex={-1} readOnly placeholder={f.placeholder} style={pv.input} />
  else if (f.fieldType === 'dropdown' || f.fieldType === 'radio') {
    control = (
      <select tabIndex={-1} className="select-compact" style={previewSelect} defaultValue="">
        <option value="">Choose…</option>
        {options.map(o => <option key={o}>{o}</option>)}
      </select>
    )
  } else {
    const type = ['email', 'number', 'date'].includes(f.fieldType) ? f.fieldType : 'text'
    control = <input type={type} tabIndex={-1} readOnly placeholder={f.placeholder} style={pv.input} />
  }
  return <label style={pv.label}>{label}{star}{control}</label>
}
