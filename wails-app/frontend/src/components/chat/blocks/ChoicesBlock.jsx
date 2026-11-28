import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Check, Square, CheckSquare } from 'lucide-react'
import { useBlockEnv } from './context.js'
import { useAnswer } from './answers.js'
import { SkipToggle, useAnswerItem } from '../../answerSet/AnswerSet.jsx'
import { useDraft } from '../../answerSet/useDraft.js'
import { answerText } from './choicesSchema.js'
import { panel, heading, note, optionButton, primaryButton, COLOR } from './ui.js'

const NO_PICK = { ids: [], skip: false }

// choices: options the user clicks to answer. The answer goes out as the
// next user message through the chat's onSend; it is then remembered (see
// answers.js) and the block shows it, locked. Without onSend the block is
// disabled with a note. When other questions wait in the same turn, a click only
// picks: the turn's AnswerSetBar sends them all together.
export function ChoicesBlock({ data, blockKey }) {
  const { t } = useTranslation()
  const { onSend, turn } = useBlockEnv()
  const [answer, remember] = useAnswer(blockKey)
  const [pick, setPick] = useDraft(`${blockKey}|pick`, NO_PICK)
  const [busy, setBusy] = useState(false)
  const locked = !!answer
  // A pick stands for the block it was made on.
  const sig = JSON.stringify(data)
  const current = pick.sig === sig
  const dropped = !current && (pick.ids.length > 0 || pick.skip)
  const picked = current ? pick.ids.filter(id => data.options.some(o => o.id === id)) : []
  const skipped = current && pick.skip
  const textOf = (ids) => answerText(data, data.options.filter(o => ids.includes(o.id)).map(o => o.label))
  const { grouped, sending } = useAnswerItem(blockKey, {
    group: !locked && onSend ? turn : '', ready: skipped || picked.length > 0, skipped,
    collect: () => (skipped ? { skip: '' } : { text: textOf(picked), remember: () => remember({ text: textOf(picked), ids: picked }) }),
  })
  const disabled = locked || !onSend || busy || sending

  const submit = async (ids) => {
    if (disabled || grouped || ids.length === 0) return
    const text = textOf(ids)
    setBusy(true)
    try {
      if (await onSend(text)) remember({ text, ids })
    } finally { setBusy(false) }
  }
  const toggle = (id) => setPick({ sig, skip: false, ids: picked.includes(id) ? picked.filter(x => x !== id) : [...picked, id] })
  // In a set a single choice is held, like a multiple one.
  const choose = (id) => (data.multi ? toggle(id) : grouped ? setPick({ sig, skip: false, ids: [id] }) : submit([id]))
  const chosenIds = locked ? (answer.ids || []) : skipped ? [] : data.multi || grouped ? picked : []

  return (
    <div role="group" aria-label={data.question || t('chatBlocks.choices')} style={panel}>
      {data.question && <p style={heading}>{data.question}</p>}
      <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
        {data.options.map((o) => {
          const chosen = chosenIds.includes(o.id)
          const state = locked ? (chosen ? 'chosen' : 'dim') : (chosen ? 'chosen' : (disabled ? 'dim' : 'idle'))
          const Icon = data.multi ? (chosen ? CheckSquare : Square) : Check
          return (
            <button key={o.id} type="button" disabled={disabled} aria-pressed={data.multi || grouped ? chosen : undefined}
              onClick={() => choose(o.id)} style={optionButton(state)}>
              <Icon size={12} style={{ flexShrink: 0, color: COLOR.accent, visibility: data.multi || chosen ? 'visible' : 'hidden' }} />
              <span style={{ flex: 1 }}>{o.label}{o.hint && <span style={{ color: COLOR.muted }}> {o.hint}</span>}</span>
            </button>
          )
        })}
      </div>
      {grouped && !locked && <SkipToggle skipped={skipped} disabled={disabled} onChange={skip => setPick({ sig, skip, ids: [] })} />}
      {!locked && dropped && <div role="alert" style={note}>{t('chatBlocks.changed')}</div>}
      {data.multi && !grouped && !locked && (
        <button type="button" disabled={disabled || picked.length === 0} onClick={() => submit(picked)} style={primaryButton(disabled || picked.length === 0)}>
          {t('chatBlocks.confirm')}
        </button>
      )}
      {locked && <div style={note}>{t('chatBlocks.answered')}</div>}
      {!locked && !onSend && <div style={note}>{t('chatBlocks.cannotSend')}</div>}
    </div>
  )
}
