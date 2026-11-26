// The input components of the A2UI catalog. Each writes to the surface's local
// data model only; a settled card disables them.
import { useId, useLayoutEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { getPointer } from '../../lib/a2uiModel.js'

export const isBinding = v => v && typeof v === 'object' && typeof v['@path'] === 'string'
// bound returns the pointer a two-way component writes to, or null if its value is a literal.
const bound = v => (isBinding(v) ? v['@path'] : null)
const current = (v, ctx) => (isBinding(v) ? getPointer(ctx.model, v['@path']) : v)

// The core caps one string sent back with an action at this many bytes.
const MAX_TYPED = 24 * 1024

// fitHeight makes a text box as tall as its text. The field keeps its height
// while the box is measured, so the transcript does not jump on each key.
function fitHeight(el) {
  const holder = el.parentElement
  const held = holder.style.minHeight
  holder.style.minHeight = `${holder.offsetHeight}px`
  el.style.height = 'auto'
  // 0 while the card is hidden: the width observer measures it once it shows.
  if (el.scrollHeight) el.style.height = `${el.scrollHeight + el.offsetHeight - el.clientHeight + 1}px`
  holder.style.minHeight = held
}

// A text field shows its whole text, never scrolled or clipped: a person must
// see all of what a button would send. It is measured after every draw (first
// one, edit, data update), when its width changes and when a font arrives.
function GrowingText(props) {
  const ref = useRef(null)
  useLayoutEffect(() => { fitHeight(ref.current) })
  useLayoutEffect(() => {
    const el = ref.current
    let width = el.clientWidth
    let frame = 0
    // A frame later: resizing inside the observer's callback is a loop error.
    const again = () => { cancelAnimationFrame(frame); frame = requestAnimationFrame(() => fitHeight(el)) }
    const observer = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(() => {
      if (el.clientWidth === width) return
      width = el.clientWidth
      again()
    })
    observer?.observe(el)
    document.fonts?.addEventListener?.('loadingdone', again)
    return () => {
      observer?.disconnect()
      document.fonts?.removeEventListener?.('loadingdone', again)
      cancelAnimationFrame(frame)
    }
  }, [])
  return <textarea ref={ref} {...props} />
}

// A one-line field wraps like any other but takes no line break from the keyboard.
const noNewline = e => { if (e.key === 'Enter' && !e.nativeEvent.isComposing) e.preventDefault() }

export function Field({ comp, ctx, str }) {
  const { t } = useTranslation()
  const uid = useId()
  const path = bound(comp.value)
  const num = comp.variant === 'number'
  const onChange = e => path && ctx.write(path, num ? (e.target.value === '' || !Number.isFinite(Number(e.target.value)) ? '' : Number(e.target.value)) : e.target.value)
  // The value is never shortened for display: the whole of it is what is sent.
  const held = current(comp.value, ctx)
  const value = held == null || typeof held === 'object' ? '' : String(held)
  const long = comp.variant === 'longText'
  const props = { id: uid, className: 'a2ui-input', value, onChange, readOnly: !path || ctx.resolved, placeholder: str(comp.placeholder), maxLength: MAX_TYPED }
  return (
    <div className="a2ui-field">
      <label htmlFor={uid} className="a2ui-label">{str(comp.label) || t('a2ui.textField')}</label>
      {num ? <input type="number" {...props} /> : <GrowingText rows={long ? 3 : 1} onKeyDown={long ? undefined : noNewline} {...props} />}
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
