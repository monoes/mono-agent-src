// Renderer for one A2UI v1.0 surface with our small trusted catalog
// (internal/a2ui/catalog.go). All text goes through React text nodes: no
// dangerouslySetInnerHTML, no markdown, no links. Typing and toggling write to
// a LOCAL data model only; only a Button press leaves the surface, as a v1.0
// action envelope handed to onAction (see CALL SITE in ChatTimeline.jsx). When
// several cards of one turn wait at once, a Button sends nothing: the card holds
// the person's choice and the turn's AnswerSetBar sends them all (#1803).
import { Component, useCallback, useEffect, useId, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { fieldMeaning, getPointer, isAskCard, setPointer } from '../../lib/a2uiModel.js'
import { SkipToggle, useAnswerItem } from '../answerSet/AnswerSet.jsx'
import { useDraft } from '../answerSet/useDraft.js'
import { Check, Choice, Field, isBinding } from './A2UIInputs.jsx'
import './a2ui.css'

export const MAX_NODES = 200
export const MAX_DEPTH = 12
const MAX_TEXT = 8000
const GUARD_MS = 3000

const JUSTIFY = { start: 'flex-start', center: 'center', end: 'flex-end', spaceBetween: 'space-between', spaceAround: 'space-around', spaceEvenly: 'space-evenly', stretch: 'stretch' }
const ALIGN = { start: 'flex-start', center: 'center', end: 'flex-end', stretch: 'stretch' }

const TONE_MARK = { info: '\u25cf', success: '\u2713', warning: '!', danger: '\u2715' }
const same = (a, b) => JSON.stringify(a) === JSON.stringify(b)
const NO_EDITS = []
const NO_PICK = { chosen: '', skip: false }

// Local edits layered over the server data model. An edit lasts until the
// server changes the value it was based on, so a progress update does not wipe
// what the user is typing, or changes what the field means.
function effectiveModel(server, edits, meaning) {
  let m = server
  for (const e of edits) {
    if (same(getPointer(server, e.path), e.base) && e.meaning === meaning(e.path)) m = setPointer(m, e.path, e.value)
  }
  return m
}

function useSurfaceModel(surface, draftKey) {
  const [edits, setEdits, drop] = useDraft(draftKey && `${draftKey}|edits`, NO_EDITS)
  const meaning = useCallback(path => fieldMeaning(surface.components, surface.dataModel, path), [surface.components, surface.dataModel])
  const model = useMemo(() => effectiveModel(surface.dataModel, edits, meaning), [surface.dataModel, edits, meaning])
  const write = useCallback((path, value) => {
    setEdits(prev => [...prev.filter(e => e.path !== path), { path, value, base: getPointer(surface.dataModel, path), meaning: meaning(path) }])
  }, [surface.dataModel, setEdits, meaning])
  const cleared = useMemo(() => edits.some(e => e.meaning !== meaning(e.path)), [edits, meaning])
  return [model, write, drop, cleared]
}

// What pressing the button sends: the v1.0 action envelope with its context read
// from the model; null when the button names no event.
// revision is the version of the card the person saw.
function envelopeOf(surfaceId, id, comp, model, revision) {
  const ev = comp?.action && comp.action.event
  if (!ev || typeof ev.name !== 'string' || !ev.name) return null
  const context = {}
  for (const [k, v] of Object.entries(ev.context && typeof ev.context === 'object' ? ev.context : {})) {
    const x = isBinding(v) ? getPointer(model, v['@path']) : v
    if (x !== undefined) context[k] = x
  }
  return { version: '1.0', action: { name: ev.name, surfaceId, sourceComponentId: id, timestamp: new Date().toISOString(), context, ...(revision ? { revision } : {}) } }
}

const filled = v => (Array.isArray(v) ? v.length > 0 : v != null && String(v).trim() !== '')

// Whether every field the button sends holds an answer.
function allFilled(comp, model) {
  return Object.values(comp?.action?.event?.context || {}).every(v => !isBinding(v) || filled(getPointer(model, v['@path'])))
}

// The one walk over a surface's component references; everything that draws or
// reads the tree uses its result. Every reference looked at (repeated, unknown
// or not even a string) spends the MAX_NODES budget, a component is placed at
// most once, and nesting stops at MAX_DEPTH. So the work does not grow with how
// often ids point at each other.
export function planTree(components) {
  const seen = new Set()
  const order = []
  let visits = 0
  let truncated = false
  const walk = (id, depth) => {
    visits += 1
    if (typeof id !== 'string' || seen.has(id) || !Object.hasOwn(components, id)) return null
    if (depth > MAX_DEPTH) { truncated = true; return null }
    seen.add(id)
    order.push(id)
    const node = { id, kids: [] }
    const comp = components[id]
    const children = comp?.children
    const refs = Array.isArray(children) ? children : comp?.child !== undefined ? [comp.child] : []
    for (let i = 0; i < refs.length; i++) {
      if (visits >= MAX_NODES) { truncated = true; break }
      const kid = walk(refs[i], depth + 1)
      if (kid) node.kids.push(kid)
    }
    return node
  }
  const root = walk('root', 0)
  return { root, order, visits, truncated }
}

function Placeholder({ children }) {
  return <div className="a2ui-unknown" data-testid="a2ui-unknown">{children}</div>
}

const textOf = (comp, model) => {
  const v = comp?.component === 'Text' ? (isBinding(comp.text) ? getPointer(model, comp.text['@path']) : comp.text) : ''
  return v == null || typeof v === 'object' ? '' : String(v).slice(0, 120)
}

function Node({ node, ctx, question }) {
  const { t } = useTranslation()
  const { id, kids } = node
  const comp = ctx.components[id]
  if (!comp || typeof comp !== 'object') return null
  const val = v => (isBinding(v) ? getPointer(ctx.model, v['@path']) : v)
  const str = v => { const x = val(v); return x == null || typeof x === 'object' ? '' : String(x).slice(0, MAX_TEXT) }
  const all = () => kids.map((k, i) => <Node key={k.id} node={k} ctx={ctx} question={textOf(ctx.components[kids[i - 1]?.id], ctx.model)} />)
  const one = () => (kids[0] ? <Node node={kids[0]} ctx={ctx} /> : null)
  const flex = dir => (
    <div className={`a2ui-${dir}`} style={{ justifyContent: JUSTIFY[comp.justify], alignItems: ALIGN[comp.align] }}>{all()}</div>
  )
  switch (comp.component) {
    case 'Text': return <p className={`a2ui-text a2ui-text-${comp.variant === 'caption' ? 'caption' : 'body'}`}>{str(comp.text)}</p>
    case 'Row': return flex('row')
    case 'Column': return flex('column')
    case 'Card': return <div className="a2ui-card">{one()}</div>
    case 'Divider': return <hr className={`a2ui-divider a2ui-divider-${comp.axis === 'vertical' ? 'v' : 'h'}`} aria-orientation={comp.axis === 'vertical' ? 'vertical' : 'horizontal'} />
    case 'Badge': {
      const tone = TONE_MARK[comp.tone] ? comp.tone : 'neutral'
      // The tone is a mark and a spoken word as well as a colour.
      return (
        <span className={`a2ui-badge a2ui-badge-${tone}`}>
          {tone !== 'neutral' && <><span aria-hidden="true" className="a2ui-mark">{TONE_MARK[tone]}</span><span className="a2ui-sr">{t(`a2ui.tone.${tone}`)}: </span></>}
          <span>{str(comp.text)}</span>
        </span>
      )
    }
    case 'Progress': return <ProgressBar value={Number(val(comp.value))} max={comp.max === undefined ? 100 : Number(val(comp.max))} label={str(comp.label)} />
    case 'Button': return <ActionButton id={id} comp={comp} ctx={ctx} label={one()} />
    case 'TextField': return <Field comp={comp} ctx={ctx} str={str} />
    case 'CheckBox': return <Check comp={comp} ctx={ctx} str={str} />
    case 'ChoicePicker': return <Choice comp={comp} ctx={ctx} str={str} question={question} />
    default: return <Placeholder>{t('a2ui.unsupported')}</Placeholder>
  }
}

function ProgressBar({ value, max, label }) {
  const { t } = useTranslation()
  const m = Number.isFinite(max) && max > 0 ? max : 100
  const v = Number.isFinite(value) ? Math.min(Math.max(value, 0), m) : 0
  return (
    <div className="a2ui-progress">
      {label && <span className="a2ui-text-caption">{label}</span>}
      <div role="progressbar" aria-label={label || t('a2ui.progress')} aria-valuemin={0} aria-valuemax={m} aria-valuenow={v} aria-valuetext={t('a2ui.progressValue', { value: v, max: m })} className="a2ui-bar"><i style={{ width: `${(v / m) * 100}%` }} /></div>
    </div>
  )
}

function ActionButton({ id, comp, ctx, label }) {
  const env = () => envelopeOf(ctx.surfaceId, id, comp, ctx.model, ctx.rev)
  const ev = comp.action && comp.action.event
  const variant = ['primary', 'borderless', 'danger'].includes(comp.variant) ? comp.variant : 'default'
  // A question's Continue has no place in a set: the set's own button sends it.
  if (ctx.grouped && ctx.asks) return null
  // aria-disabled, not disabled: the button keeps keyboard focus while it waits.
  const unanswered = ctx.asks && !ctx.resolved && !allFilled(comp, ctx.model)
  const off = ctx.locked || !ev || unanswered
  const click = () => {
    const envelope = env()
    if (!envelope) return
    if (ctx.grouped) ctx.choose(id)
    else ctx.press(envelope)
  }
  return <button type="button" className={`a2ui-btn a2ui-btn-${variant}`} aria-disabled={off || undefined} aria-pressed={ctx.grouped ? ctx.chosen === id : undefined} aria-describedby={ctx.locked || unanswered ? ctx.reasonId : undefined} onClick={off ? undefined : click}>{label}</button>
}

class Boundary extends Component {
  state = { failed: false }
  static getDerivedStateFromError() { return { failed: true } }
  componentDidCatch(err) { console.warn('a2ui: surface failed to render', err) }
  render() {
    return this.state.failed ? this.props.fallback : this.props.children
  }
}

function SurfaceBody({ surface, plan, onAction, group, live }) {
  const { t } = useTranslation()
  const reasonId = useId()
  const asks = isAskCard(surface)
  // A draft belongs to one question card of one turn; approvals keep none.
  const draftKey = group && (asks || surface.provenance === 'agent') ? `${group}|${surface.surfaceId}` : ''
  const [model, write, dropEdits, cleared] = useSurfaceModel(surface, draftKey)
  const [pick, setPick, dropPick] = useDraft(draftKey && `${draftKey}|pick`, NO_PICK)
  const [pressed, setPressed] = useState(false)
  const timer = useRef(null)
  const [working, setWorking] = useState(false) // the press has not answered yet
  const inFlight = useRef(false)
  const guard = useRef(false) // two clicks in one frame see the same render
  const unlock = useCallback(() => { if (!inFlight.current) { guard.current = false; setPressed(false) } }, [])
  // The surface changing (a new message) or 3s unlocks the buttons again, but
  // never while a press is still running: it may be sending.
  useEffect(() => { unlock() }, [surface.lastSeq, unlock])
  useEffect(() => () => clearTimeout(timer.current), [])
  const press = useCallback(envelope => {
    if (guard.current) return
    guard.current = true
    setPressed(true)
    clearTimeout(timer.current)
    const cooldown = () => { timer.current = setTimeout(unlock, GUARD_MS) }
    const done = onAction(envelope)
    if (typeof done?.then !== 'function') { cooldown(); return }
    inFlight.current = true
    setWorking(true)
    const answered = () => { inFlight.current = false; setWorking(false); cooldown() }
    done.then(answered, answered)
  }, [onAction, unlock])
  const resolved = surface.status === 'resolved'
  const buttons = plan.order.filter(id => surface.components[id]?.component === 'Button' && surface.components[id].action?.event?.name)
  // Waiting for the person: a worker's question while its turn runs, or an agent's card with a button.
  const waiting = !resolved && buttons.length > 0 && (asks ? live : surface.provenance === 'agent')
  // A pick or a skip stands for the card as it was when made: any change drops it.
  const current = pick.rev === surface.rev
  const dropped = !current && (!!pick.chosen || !!pick.skip)
  const chosen = current && buttons.includes(pick.chosen) ? pick.chosen : ''
  const skipped = !asks && current && !!pick.skip
  const ready = asks ? waiting && allFilled(surface.components[buttons[0]], model) : skipped || !!chosen
  const collect = () => {
    if (skipped) return { skip: surface.surfaceId }
    const id = asks ? buttons[0] : chosen
    const envelope = envelopeOf(surface.surfaceId, id, surface.components[id], model, surface.rev)
    return envelope && { action: envelope.action }
  }
  const { grouped, sending } = useAnswerItem(draftKey, { group: waiting && draftKey ? group : '', ready, skipped, collect })
  useEffect(() => { if (resolved || buttons.length === 0) { dropEdits(); dropPick() } }, [resolved, buttons.length, dropEdits, dropPick])
  const locked = pressed || resolved || sending
  const choose = id => setPick({ chosen: chosen === id ? '' : id, skip: false, rev: surface.rev })
  const ctx = { components: surface.components, model, write, press, surfaceId: surface.surfaceId, locked, resolved, reasonId, asks, grouped, chosen: skipped ? '' : chosen, choose, rev: surface.rev }
  const hasButton = plan.order.some(id => surface.components[id]?.component === 'Button')
  const unanswered = asks && waiting && !grouped && !ready
  return (
    <>
      {plan.root && <Node node={plan.root} ctx={ctx} />}
      {plan.truncated && <Placeholder>{t('a2ui.truncated')}</Placeholder>}
      {grouped && <p className="a2ui-reason">{t(asks ? (ready ? 'a2ui.setReady' : 'a2ui.setAnswer') : (skipped ? 'a2ui.setSkipped' : chosen ? 'a2ui.setChosen' : 'a2ui.setChoose'))}</p>}
      {grouped && !asks && <SkipToggle skipped={skipped} disabled={locked} onChange={skip => setPick({ chosen: '', skip, rev: surface.rev })} />}
      {!resolved && (dropped || cleared) && <p role="alert" className="a2ui-reason a2ui-changed">{t(dropped ? 'a2ui.pickDropped' : 'a2ui.typedDropped')}</p>}
      {locked && hasButton && !(grouped && asks) && <p id={reasonId} className="a2ui-reason">{t(resolved ? 'a2ui.lockedDone' : working || sending ? 'a2ui.lockedWorking' : 'a2ui.lockedSent')}</p>}
      {!locked && unanswered && <p id={reasonId} className="a2ui-reason">{t('a2ui.answerFirst')}</p>}
    </>
  )
}

// What a screen reader hears about a card that changed: its new layout or that
// it was answered. A data-model update (a progress tick) says nothing.
function useAnnouncement(surface, plan, title) {
  const { t } = useTranslation()
  const [text, setText] = useState('')
  // The placed components only; their child lists are already in the order.
  const shape = JSON.stringify(plan.order.map(id => { const { children: _, ...own } = surface.components[id] || {}; return own }))
  const last = useRef({ shape, status: surface.status })
  useEffect(() => {
    const was = last.current
    last.current = { shape, status: surface.status }
    if (surface.status === 'resolved' && was.status !== 'resolved') setText(t('a2ui.announceDone', { title }))
    else if (shape !== was.shape) setText(t('a2ui.announceUpdated', { title }))
  }, [shape, surface.status, title, t])
  return text
}

// The card's title: its first piece of text (or badge) in reading order.
function titleOf(surface, plan) {
  for (const id of plan.order) {
    const c = surface.components[id]
    const text = textOf(c?.component === 'Badge' ? { component: 'Text', text: c.text } : c, surface.dataModel)
    if (text) return text
  }
  return ''
}

const noop = envelope => console.info('a2ui action (no handler wired)', envelope)

function SurfaceCard({ surface, onAction, group, live }) {
  const { t } = useTranslation()
  const from = t(surface.provenance === 'system' ? 'a2ui.fromSystem' : 'a2ui.fromAgent')
  const plan = useMemo(() => planTree(surface.components), [surface.components])
  const title = titleOf(surface, plan)
  const said = useAnnouncement(surface, plan, title)
  return (
    <section className="a2ui-surface" data-testid="a2ui-surface" data-surface-id={surface.surfaceId} data-status={surface.status} aria-label={title ? `${from}: ${title}` : from}>
      <div className="a2ui-prov">{from}{surface.status === 'resolved' ? ` \u00b7 ${t('a2ui.done')}` : ''}</div>
      <SurfaceBody surface={surface} plan={plan} onAction={onAction} group={group} live={live} />
      <div role="status" aria-live="polite" className="a2ui-sr">{said}</div>
    </section>
  )
}

// Anything that throws while drawing a card ends at this card, not the chat.
// group is the turn the card belongs to ('' outside a chat); live says that turn
// is still running, which is when a worker's question can be answered.
export function A2UISurface({ surface, onAction = noop, group = '', live = false }) {
  const { t } = useTranslation()
  const fallback = <section className="a2ui-surface" data-testid="a2ui-surface"><Placeholder>{t('a2ui.failed')}</Placeholder></section>
  return (
    <Boundary key={surface.surfaceId} fallback={fallback}><SurfaceCard surface={surface} onAction={onAction} group={group} live={live} /></Boundary>
  )
}
