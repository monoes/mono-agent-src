import { useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { BellRing, Loader, Send, ExternalLink } from 'lucide-react'
import { MuteToggle, PersonItemCard } from './PersonItemCard.jsx'
import './personitem.css'

// AskItemCard is the answer card of a critical ask (item kind ancestor_question): a lead asked a session above
// its parent something very important, and only the person answers. The question is text another agent wrote:
// it is rendered as plain text (a React text node, never markup), and the answer goes back to the asking lead as
// quoted data. onAnswer('answerAsk', item, text) resolves to the CLI's document or { error }; the card shows a
// refusal where it was asked and keeps the typed text.
export function AskItemCard({ item, onAnswer, onOpenAgent = null, showMute = true }) {
  const { t } = useTranslation()
  const [text, setText] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const box = useRef(null)
  const asker = item.askerTitle || `#${item.sessionId}`
  const ancestor = item.ancestorTitle || ''

  const submit = async () => {
    const words = text.trim()
    if (!words || busy) return
    setBusy(true); setError('')
    const res = await onAnswer('answerAsk', item, words)
    setBusy(false)
    if (res?.error) setError(String(res.error))
  }

  return (
    <section className="pi-card" role="group" aria-label={t('personItem.askCardLabel', { asker })} data-testid="person-item-card" data-kind={item.kind} aria-busy={busy}>
      <header className="pi-head">
        <BellRing size={12} aria-hidden="true" />
        <span className="pi-head-title">{t('personItem.askHeading')}</span>
        {showMute && <MuteToggle />}
      </header>
      <h3 className="pi-title" data-testid="ask-parties">{t('personItem.askParties', { asker, ancestor })}</h3>
      <div className="pi-why-head">{t('personItem.askQuestion')}</div>
      <p className="pi-question" data-testid="ask-question">{item.question}</p>
      <label className="pi-field">
        <span>{t('personItem.answerLabel')}</span>
        <textarea ref={box} value={text} rows={3} disabled={busy} onChange={e => setText(e.target.value)}
          onKeyDown={e => { if (e.key === 'Enter' && (e.ctrlKey || e.metaKey) && !e.nativeEvent?.isComposing) { e.preventDefault(); submit() } }} />
      </label>
      <div className="pi-actions">
        <button type="button" className="btn btn-primary btn-sm" disabled={busy || !text.trim()} onClick={submit}>
          {busy ? <Loader size={11} className="spin" aria-hidden="true" /> : <Send size={11} aria-hidden="true" />} {t('personItem.answerSend')}
        </button>
        {onOpenAgent && item.conversationId && (
          <button type="button" className="btn btn-ghost btn-sm pi-open" onClick={() => onOpenAgent(item)}>
            <ExternalLink size={11} aria-hidden="true" /> {t('personItem.openAgent')}
          </button>
        )}
      </div>
      {error && <div className="pi-error" role="alert">{t('personItem.failed', { error })}</div>}
    </section>
  )
}

// PersonCard picks the card for an item's kind.
export function PersonCard(props) {
  return props.item?.kind === 'ancestor_question' ? <AskItemCard {...props} /> : <PersonItemCard {...props} />
}
