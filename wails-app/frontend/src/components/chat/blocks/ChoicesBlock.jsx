import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Check, Square, CheckSquare } from 'lucide-react'
import { useBlockEnv } from './context.js'
import { useAnswer } from './answers.js'
import { answerText } from './choicesSchema.js'
import { panel, heading, note, optionButton, primaryButton, COLOR } from './ui.js'

// choices: options the user clicks to answer. The answer goes out as the
// next user message through the chat's onSend; it is then remembered (see
// answers.js) and the block shows it, locked. Without onSend the block is
// disabled with a note.
export function ChoicesBlock({ data, blockKey }) {
  const { t } = useTranslation()
  const { onSend } = useBlockEnv()
  const [answer, remember] = useAnswer(blockKey)
  const [picked, setPicked] = useState([])
  const [busy, setBusy] = useState(false)
  const locked = !!answer
  const disabled = locked || !onSend || busy

  const submit = async (ids) => {
    if (disabled || ids.length === 0) return
    const labels = data.options.filter(o => ids.includes(o.id)).map(o => o.label)
    const text = answerText(data, labels)
    setBusy(true)
    try {
      if (await onSend(text)) remember({ text, ids })
    } finally { setBusy(false) }
  }
  const toggle = (id) => setPicked(p => (p.includes(id) ? p.filter(x => x !== id) : [...p, id]))
  const chosenIds = locked ? (answer.ids || []) : picked

  return (
    <div role="group" aria-label={data.question || t('chatBlocks.choices')} style={panel}>
      {data.question && <p style={heading}>{data.question}</p>}
      <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
        {data.options.map((o) => {
          const chosen = chosenIds.includes(o.id)
          const state = locked ? (chosen ? 'chosen' : 'dim') : (chosen ? 'chosen' : (disabled ? 'dim' : 'idle'))
          const Icon = data.multi ? (chosen ? CheckSquare : Square) : Check
          return (
            <button key={o.id} type="button" disabled={disabled} aria-pressed={data.multi ? chosen : undefined}
              onClick={() => (data.multi ? toggle(o.id) : submit([o.id]))} style={optionButton(state)}>
              <Icon size={12} style={{ flexShrink: 0, color: COLOR.accent, visibility: data.multi || chosen ? 'visible' : 'hidden' }} />
              <span style={{ flex: 1 }}>{o.label}{o.hint && <span style={{ color: COLOR.muted }}> {o.hint}</span>}</span>
            </button>
          )
        })}
      </div>
      {data.multi && !locked && (
        <button type="button" disabled={disabled || picked.length === 0} onClick={() => submit(picked)} style={primaryButton(disabled || picked.length === 0)}>
          {t('chatBlocks.confirm')}
        </button>
      )}
      {locked && <div style={note}>{t('chatBlocks.answered')}</div>}
      {!locked && !onSend && <div style={note}>{t('chatBlocks.cannotSend')}</div>}
    </div>
  )
}
