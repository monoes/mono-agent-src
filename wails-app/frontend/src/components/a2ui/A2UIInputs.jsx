// The input components of the A2UI catalog. Each writes to the surface's local
// data model only; a settled card disables them.
import { useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { getPointer } from '../../lib/a2uiModel.js'

export const isBinding = v => v && typeof v === 'object' && typeof v['@path'] === 'string'
// bound returns the pointer a two-way component writes to, or null if its value is a literal.
const bound = v => (isBinding(v) ? v['@path'] : null)
const current = (v, ctx) => (isBinding(v) ? getPointer(ctx.model, v['@path']) : v)

export function Field({ comp, ctx, str }) {
  const { t } = useTranslation()
  const uid = useId()
  const path = bound(comp.value)
  const num = comp.variant === 'number'
  const onChange = e => path && ctx.write(path, num ? (e.target.value === '' || !Number.isFinite(Number(e.target.value)) ? '' : Number(e.target.value)) : e.target.value)
  const props = { id: uid, className: 'a2ui-input', value: str(comp.value), onChange, readOnly: !path || ctx.resolved, placeholder: str(comp.placeholder), maxLength: 4000 }
  return (
    <div className="a2ui-field">
      <label htmlFor={uid} className="a2ui-label">{str(comp.label) || t('a2ui.textField')}</label>
      {comp.variant === 'longText' ? <textarea rows={3} {...props} /> : <input type={num ? 'number' : 'text'} {...props} />}
    </div>
  )
}

export function Check({ comp, ctx, str }) {
  const { t } = useTranslation()
  const path = bound(comp.value)
  return (
    <label className="a2ui-check">
      <input type="checkbox" checked={current(comp.value, ctx) === true} disabled={!path || ctx.resolved} onChange={e => ctx.write(path, e.target.checked)} />
      <span>{str(comp.label) || t('a2ui.checkBox')}</span>
    </label>
  )
}

// question is the text shown just before the picker, if any: it names the group.
export function Choice({ comp, ctx, str, question }) {
  const { t } = useTranslation()
  const uid = useId()
  const [filter, setFilter] = useState('')
  const path = bound(comp.value)
  const raw = current(comp.value, ctx)
  const sel = Array.isArray(raw) ? raw.map(String) : []
  const multi = comp.variant === 'multipleSelection'
  const all = (Array.isArray(comp.options) ? comp.options : []).slice(0, 20).filter(o => o && typeof o.value === 'string')
  const needle = filter.trim().toLowerCase()
  // A chosen option stays listed whatever the filter says, so a choice is never hidden.
  const shown = comp.filterable === true && needle ? all.filter(o => sel.includes(o.value) || str(o.label).toLowerCase().includes(needle)) : all
  const toggle = v => {
    if (!path) return
    ctx.write(path, multi ? (sel.includes(v) ? sel.filter(x => x !== v) : [...sel, v]) : [v])
  }
  return (
    <fieldset className="a2ui-fieldset">
      <legend className="a2ui-sr">{question || t(multi ? 'a2ui.chooseAny' : 'a2ui.chooseOne')}</legend>
      {comp.filterable === true && (
        <div className="a2ui-field">
          <label htmlFor={`${uid}-f`} className="a2ui-label">{t('a2ui.filterOptions')}</label>
          <input id={`${uid}-f`} type="search" className="a2ui-input" value={filter} disabled={ctx.resolved} onChange={e => setFilter(e.target.value)} />
          <span role="status" className="a2ui-sr">{needle ? t('a2ui.optionsShown', { shown: shown.length, total: all.length }) : ''}</span>
        </div>
      )}
      <div className={`a2ui-choices a2ui-choices-${comp.displayStyle === 'chips' ? 'chips' : 'list'}`}>
        {shown.map(o => (
          <label key={o.value} className={`a2ui-choice${sel.includes(o.value) ? ' on' : ''}`}>
            <input type={multi ? 'checkbox' : 'radio'} name={uid} checked={sel.includes(o.value)} disabled={!path || ctx.resolved} onChange={() => toggle(o.value)} />
            <span>{str(o.label)}</span>
          </label>
        ))}
        {shown.length === 0 && <span className="a2ui-text-caption">{t('a2ui.noOptions')}</span>}
      </div>
    </fieldset>
  )
}
