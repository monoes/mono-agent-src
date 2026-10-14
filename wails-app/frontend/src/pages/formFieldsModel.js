// The form builder's rules: reading a node's formFields (trigger.form, the
// Wait node's form, form pages) into editable fields, checking them, and
// writing them back. Pure functions so they are unit-testable without the
// inspector.
//
// Accepted input, as the engine accepts it (internal/workflow/form_spec.go):
// a list of fields, n8n's {values: [...]} wrapper, or either as JSON text.
// Each field takes n8n's keys (fieldLabel, fieldType, fieldOptions,
// requiredField, placeholder) or the aliases label, type, options, required.
// fieldOptions is a list of strings, a comma-separated string or n8n's
// {values: [{option}]}.
//
// Output is always a list of fields with n8n key names; fieldOptions is
// written as {values: [{option}]}, the shape every form node reads. Keys the
// editor does not know (n8n has more per field) are kept as they were.

// The field types the engine serves.
export const FORM_FIELD_TYPES = ['text', 'email', 'number', 'textarea', 'dropdown', 'date', 'checkbox']

// Types whose choices the editor lists. A dropdown needs at least one; a
// checkbox with options is a group of boxes, without them a single box.
export const OPTION_TYPES = new Set(['dropdown', 'checkbox', 'radio'])
const NEEDS_OPTIONS = new Set(['dropdown', 'radio'])

const ALIASES = { label: 'fieldLabel', type: 'fieldType', options: 'fieldOptions', required: 'requiredField' }

let nextId = 1
const newId = () => `ff${nextId++}`

// optionList reads choices in any accepted shape as [{option, ...rest}].
function optionList(v) {
  if (typeof v === 'string') {
    return v.split(',').map(s => s.trim()).filter(Boolean).map(option => ({ option }))
  }
  if (v && typeof v === 'object' && !Array.isArray(v)) return optionList(v.values)
  if (!Array.isArray(v)) return []
  const out = []
  for (const o of v) {
    if (typeof o === 'string') out.push({ option: o })
    else if (o && typeof o === 'object' && typeof o.option === 'string') out.push({ ...o })
  }
  return out
}

// toEditable turns one stored field object into the editor's shape: the
// known values pulled out, everything else kept in `rest`.
function toEditable(obj) {
  const rest = {}
  for (const [k, v] of Object.entries(obj)) {
    if (k in ALIASES || k === 'fieldLabel' || k === 'fieldType' || k === 'fieldOptions' ||
      k === 'requiredField' || k === 'placeholder') continue
    rest[k] = v
  }
  const str = (...keys) => {
    for (const k of keys) if (typeof obj[k] === 'string' && obj[k].trim() !== '') return obj[k]
    return ''
  }
  const options = optionList(obj.fieldOptions)
  return {
    id: newId(),
    fieldLabel: str('fieldLabel', 'label'),
    fieldType: str('fieldType', 'type') || 'text',
    placeholder: typeof obj.placeholder === 'string' ? obj.placeholder : '',
    requiredField: obj.requiredField === true || obj.required === true,
    options: options.length ? options : optionList(obj.options),
    // Which optional keys the stored field had, so writing it back does not
    // add requiredField: false or placeholder: "" to fields that had none.
    had: {
      requiredField: 'requiredField' in obj || 'required' in obj,
      placeholder: 'placeholder' in obj,
      fieldOptions: 'fieldOptions' in obj || 'options' in obj,
    },
    rest,
  }
}

// parseFormFields reads a stored formFields value. It returns {fields} or
// {error} when the value is not something the editor can show (bad JSON, a
// {{ template }}, a list item that is not an object); the inspector then
// keeps the raw JSON editor.
export function parseFormFields(value) {
  let raw = value
  if (raw == null || raw === '') return { fields: [] }
  if (typeof raw === 'string') {
    const s = raw.trim()
    if (!s) return { fields: [] }
    if (s.includes('{{')) return { error: 'formFields holds a {{ template }}; edit it as JSON.' }
    try { raw = JSON.parse(s) } catch (e) { return { error: 'Not valid JSON: ' + e.message } }
  }
  if (raw && typeof raw === 'object' && !Array.isArray(raw)) raw = raw.values ?? []
  if (!Array.isArray(raw)) return { error: 'formFields must be a list of fields.' }
  const fields = []
  for (let i = 0; i < raw.length; i++) {
    const item = raw[i]
    if (!item || typeof item !== 'object' || Array.isArray(item)) {
      return { error: `Field ${i + 1} is not an object.` }
    }
    fields.push(toEditable(item))
  }
  return { fields }
}

// cleanOptions drops blank choices (a row just added and not typed yet).
function cleanOptions(options) {
  return (options || []).filter(o => String(o.option ?? '').trim() !== '')
    .map(o => ({ ...o, option: String(o.option).trim() }))
}

// serializeFormFields writes editor fields back as the stored list.
export function serializeFormFields(fields) {
  return (fields || []).map(f => {
    const out = { ...f.rest, fieldLabel: String(f.fieldLabel ?? '').trim(), fieldType: f.fieldType || 'text' }
    if (f.requiredField || f.had?.requiredField) out.requiredField = !!f.requiredField
    if (f.placeholder || f.had?.placeholder) out.placeholder = f.placeholder || ''
    const options = cleanOptions(f.options)
    if (options.length || f.had?.fieldOptions) out.fieldOptions = { values: options }
    return out
  })
}

// validateFormFields returns {[fieldId]: message} for fields the engine
// would reject: no label, a label used twice, a dropdown without choices.
export function validateFormFields(fields) {
  const errors = {}
  const seen = new Map()
  for (const f of fields || []) {
    const label = String(f.fieldLabel ?? '').trim()
    if (!label) { errors[f.id] = 'Give the field a label.'; continue }
    if (seen.has(label)) { errors[f.id] = `Another field is already labelled "${label}".`; continue }
    seen.set(label, f.id)
    if (NEEDS_OPTIONS.has(f.fieldType) && cleanOptions(f.options).length === 0) {
      errors[f.id] = 'Add at least one option.'
    }
  }
  return errors
}

// newField is the field "Add field" appends.
export function newField() {
  return { id: newId(), fieldLabel: '', fieldType: 'text', placeholder: '', requiredField: false, options: [], had: {}, rest: {} }
}

// moveItem returns a copy of list with item i moved by delta (−1 up, +1
// down); out-of-range moves return the list unchanged.
export function moveItem(list, i, delta) {
  const j = i + delta
  if (j < 0 || j >= list.length) return list
  const next = list.slice()
  ;[next[i], next[j]] = [next[j], next[i]]
  return next
}
