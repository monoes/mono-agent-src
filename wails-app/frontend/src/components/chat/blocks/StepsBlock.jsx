import { useTranslation } from 'react-i18next'
import { Circle, Loader, CheckCircle2, XCircle } from 'lucide-react'
import { obj, optStr, str, oneOf, arr } from './schema.js'
import { panel, heading, COLOR } from './ui.js'

export const STEP_STATES = ['todo', 'active', 'done', 'failed']

// steps: {title?, steps:[{label, state, detail?}]} 1-30 steps.
export function validateSteps(j) {
  const o = obj(j)
  return {
    title: optStr(o, 'title'),
    steps: arr(o.steps, 1, 30).map((s) => {
      const p = obj(s)
      return { label: str(p.label), state: oneOf(p.state, STEP_STATES), detail: optStr(p, 'detail') }
    }),
  }
}

const ICON = { todo: Circle, active: Loader, done: CheckCircle2, failed: XCircle }
const TINT = { todo: COLOR.muted, active: COLOR.accent, done: COLOR.ok, failed: COLOR.error }

// steps: a checklist with a progress bar; each state has its own icon and a
// text label for screen readers (never color alone).
export function StepsBlock({ data }) {
  const { t } = useTranslation()
  const done = data.steps.filter(s => s.state === 'done').length
  const total = data.steps.length
  return (
    <div style={panel}>
      {data.title && <p style={heading}>{data.title}</p>}
      <div role="progressbar" aria-valuemin={0} aria-valuemax={total} aria-valuenow={done} aria-label={t('chatBlocks.steps.progress', { done, total })}
        style={{ height: 4, borderRadius: 2, background: COLOR.border, marginBottom: 8, overflow: 'hidden' }}>
        <div style={{ width: `${(done / total) * 100}%`, height: '100%', background: COLOR.ok }} />
      </div>
      <ol style={{ listStyle: 'none', margin: 0, padding: 0, display: 'flex', flexDirection: 'column', gap: 4 }}>
        {data.steps.map((s, i) => {
          const Icon = ICON[s.state]
          return (
            <li key={i} style={{ position: 'relative', display: 'flex', gap: 8, alignItems: 'flex-start' }}>
              <Icon size={13} aria-hidden="true" className={s.state === 'active' ? 'chat-spin' : undefined} style={{ color: TINT[s.state], flexShrink: 0, marginTop: 1 }} />
              <div>
                <span style={{ color: s.state === 'todo' ? COLOR.muted : COLOR.text, textDecoration: s.state === 'done' ? 'line-through' : 'none', textDecorationColor: COLOR.muted }}>{s.label}</span>
                <span style={{ position: 'absolute', width: 1, height: 1, overflow: 'hidden' }}> ({t(`chatBlocks.steps.state.${s.state}`)})</span>
                {s.detail && <div style={{ fontSize: 10, color: COLOR.muted }}>{s.detail}</div>}
              </div>
            </li>
          )
        })}
      </ol>
    </div>
  )
}
