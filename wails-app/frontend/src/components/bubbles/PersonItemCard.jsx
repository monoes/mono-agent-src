import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { BellRing, Bell, BellOff, Check, Loader, MessageCircleQuestion, Undo2, ExternalLink } from 'lucide-react'
import { budgetSharePercent } from '../../lib/personItems.js'
import { useNotifySound } from './useNotifySound.js'
import './personitem.css'

const money = n => `$${(Number(n) || 0).toFixed(2)}`

// MuteToggle flips the operator's notifySound setting: the chime only, never the card.
export function MuteToggle() {
  const { t } = useTranslation()
  const [on, setOn] = useNotifySound()
  const Icon = on ? Bell : BellOff
  return (
    <button type="button" className="pi-mute" aria-pressed={!on} onClick={() => setOn(!on)}
      title={on ? t('personItem.mute') : t('personItem.unmute')} aria-label={on ? t('personItem.mute') : t('personItem.unmute')}>
      <Icon size={12} aria-hidden="true" />
    </button>
  )
}

// PersonItemCard is the answer card of a giant plan that waits for the person (item kind
// plan_approval): what it is, why it needs the person, and the three answers. Approve is one
// press; Deny and Ask more open a text box. onAnswer(verb, item, text) resolves to the CLI's
// document or { error }; the card shows a refusal where it was asked and keeps the typed text.
export function PersonItemCard({ item, onAnswer, onOpenAgent = null, showMute = true }) {
  const { t } = useTranslation()
  const [mode, setMode] = useState(null) // null | 'deny' | 'ask'
  const [text, setText] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [asked, setAsked] = useState(false)
  const box = useRef(null)
  const buttons = useRef({})
  const opener = useRef('')
  const title = item.title || `#${item.sessionId}`
  const reasons = item.reasons || []
  const pct = budgetSharePercent(item)

  // The box takes focus when it opens; closing it hands focus back to the button that opened it.
  useEffect(() => {
    if (mode) box.current?.focus()
    else if (opener.current) buttons.current[opener.current]?.focus()
  }, [mode])

  const open = next => { opener.current = next; setError(''); setAsked(false); setMode(next) }
  const close = () => { setMode(null); setError('') }
  const send = async (verb, words) => {
    setBusy(true); setError('')
    const res = await onAnswer(verb, item, words)
    setBusy(false)
    if (res?.error) { setError(String(res.error)); return }
    if (verb === 'ask') { setAsked(true); setText(''); setMode(null) }
  }
  const submit = () => { if (text.trim() && !busy) send(mode, text.trim()) }

  return (
    <section className="pi-card" role="group" aria-label={t('personItem.cardLabel', { title })} data-testid="person-item-card" data-kind={item.kind} aria-busy={busy}>
      <header className="pi-head">
        <BellRing size={12} aria-hidden="true" />
        <span className="pi-head-title">{t('personItem.heading')}</span>
        {showMute && <MuteToggle />}
      </header>
      <h3 className="pi-title">{title}</h3>
      {item.goal && <p className="pi-goal">{item.goal}</p>}
      {reasons.length > 0 && (
        <div className="pi-why">
          <div className="pi-why-head">{t('personItem.why')}</div>
          <ul>{reasons.map((r, i) => <li key={i}>{r}</li>)}</ul>
        </div>
      )}
      <dl className="pi-facts">
        <div><dt>{t('personItem.tasks')}</dt><dd>{item.taskCount ?? 0}</dd></div>
        <div><dt>{t('personItem.budget')}</dt><dd>{item.budgetUsd > 0 ? `${money(item.budgetUsd)}${pct ? ` · ${t('personItem.share', { pct })}` : ''}` : t('personItem.noBudget')}</dd></div>
        {item.access && <div><dt>{t('personItem.access')}</dt><dd>{item.access}</dd></div>}
      </dl>
      {mode && (
        <label className="pi-field">
          <span>{mode === 'deny' ? t('personItem.denyLabel') : t('personItem.askLabel')}</span>
          <textarea ref={box} value={text} rows={3} disabled={busy} onChange={e => setText(e.target.value)}
            onKeyDown={e => {
              if (e.key === 'Escape') { e.stopPropagation(); close() }
              else if (e.key === 'Enter' && (e.ctrlKey || e.metaKey) && !e.nativeEvent?.isComposing) { e.preventDefault(); submit() }
            }} />
        </label>
      )}
      <div className="pi-actions">
        {!mode && (
          <>
            <button type="button" className="btn btn-primary btn-sm" disabled={busy} onClick={() => send('approve')}>
              {busy ? <Loader size={11} className="spin" aria-hidden="true" /> : <Check size={11} aria-hidden="true" />} {t('personItem.approve')}
            </button>
            <button type="button" className="btn btn-ghost btn-sm" disabled={busy} ref={el => { buttons.current.deny = el }} onClick={() => open('deny')}>
              <Undo2 size={11} aria-hidden="true" /> {t('personItem.deny')}
            </button>
            <button type="button" className="btn btn-ghost btn-sm" disabled={busy} ref={el => { buttons.current.ask = el }} onClick={() => open('ask')}>
              <MessageCircleQuestion size={11} aria-hidden="true" /> {t('personItem.ask')}
            </button>
          </>
        )}
        {mode && (
          <>
            <button type="button" className="btn btn-primary btn-sm" disabled={busy || !text.trim()} onClick={submit}>
              {busy && <Loader size={11} className="spin" aria-hidden="true" />} {mode === 'deny' ? t('personItem.sendDeny') : t('personItem.sendAsk')}
            </button>
            <button type="button" className="btn btn-ghost btn-sm" disabled={busy} onClick={close}>{t('personItem.cancel')}</button>
          </>
        )}
        {onOpenAgent && item.conversationId && (
          <button type="button" className="btn btn-ghost btn-sm pi-open" onClick={() => onOpenAgent(item)}>
            <ExternalLink size={11} aria-hidden="true" /> {t('personItem.openAgent')}
          </button>
        )}
      </div>
      {asked && <div className="pi-note" role="status">{t('personItem.asked')}</div>}
      {error && <div className="pi-error" role="alert">{t('personItem.failed', { error })}</div>}
    </section>
  )
}
