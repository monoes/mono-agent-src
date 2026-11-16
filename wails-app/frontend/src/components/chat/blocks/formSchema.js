import { obj, str, optStr, opt, num, bool, oneOf, arr, unique, bad } from './schema.js'

export const FIELD_TYPES = ['text', 'number', 'select', 'checkbox']
export const MAX_FIELDS = 12
const ID = /^[A-Za-z][A-Za-z0-9_]{0,31}$/

function field(f) {
  const p = obj(f)
  const type = oneOf(p.type, FIELD_TYPES)
  const out = {
    id: str(p.id, { re: ID, max: 32 }), type, label: str(p.label, { max: 80 }),
    required: opt(p, 'required') === undefined ? false : bool(p.required),
    placeholder: optStr(p, 'placeholder', { max: 80 }),
  }
  const def = opt(p, 'default')
  if (type === 'select') {
    out.options = arr(p.options, 2, 20).map((o) => { const q = obj(o); return { value: str(q.value), label: str(q.label) } })
    unique(out.options, 'value')
    if (def !== undefined && !out.options.some(o => o.value === def)) bad('default not an option')
    out.default = def === undefined ? '' : def
  } else if (type === 'number') {
    const min = opt(p, 'min'), max = opt(p, 'max')
    out.min = min === undefined ? undefined : num(min)
    out.max = max === undefined ? undefined : num(max)
    if (def !== undefined) num(def)
    out.default = def === undefined ? '' : String(def)
  } else if (type === 'checkbox') {
    out.default = def === undefined ? false : bool(def)
  } else {
    out.default = def === undefined ? '' : str(def)
  }
  return out
}

// form: {title?, submitLabel?, fields:[field]} (see the spec).
export function validateForm(j) {
  const o = obj(j)
  const fields = arr(o.fields, 1, MAX_FIELDS).map(field)
  unique(fields, 'id')
  return { title: optStr(o, 'title'), submitLabel: optStr(o, 'submitLabel', { max: 30 }), fields }
}

// formErrors: field id -> reason ('required' | 'number' | 'range'), for the
// values the user entered. Empty object = submittable.
export function formErrors(fields, values) {
  const err = {}
  for (const f of fields) {
    const v = values[f.id]
    if (f.type === 'checkbox') { if (f.required && !v) err[f.id] = 'required'; continue }
    if (v === '' || v === undefined) { if (f.required) err[f.id] = 'required'; continue }
    if (f.type === 'number') {
      const n = Number(v)
      if (!Number.isFinite(n)) err[f.id] = 'number'
      else if ((f.min !== undefined && n < f.min) || (f.max !== undefined && n > f.max)) err[f.id] = 'range'
    }
  }
  return err
}

// formMessage: the user message a submit is sent as.
export function formMessage(data, values) {
  const line = (f) => {
    const v = values[f.id]
    const shown = f.type === 'checkbox' ? (v ? 'yes' : 'no')
      : f.type === 'select' ? (f.options.find(o => o.value === v)?.label ?? '-')
        : (String(v ?? '').replace(/\s+/g, ' ').trim() || '-')
    return `${f.label}: ${shown}`
  }
  return [data.title || 'Form', ...data.fields.map(line)].join('\n')
}
