import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { BellRing, ExternalLink } from 'lucide-react'
import { itemsForConversation } from '../../lib/personItems.js'
import { usePersonItemsContext } from './PersonItemsContext.jsx'
import { MuteToggle, PersonItemCard } from './PersonItemCard.jsx'
import './personitem.css'

const isPlan = it => it?.kind === 'plan_approval' && it.sessionId > 0

// PersonItemsRegion is the answer cards in one agent's pane (root or lite): the plan items of the
// session this chat is the agent of, above the transcript so they are seen at once.
export function PersonItemsRegion({ conversationId }) {
  const { t } = useTranslation()
  const { items, answer } = usePersonItemsContext()
  const mine = itemsForConversation(items, conversationId).filter(isPlan)
  if (mine.length === 0) return null
  return (
    <div className="pi-region" data-testid="person-items-pane" role="region" aria-label={t('personItem.regionLabel')} aria-live="polite">
      {mine.map(it => <PersonItemCard key={it.id} item={it} onAnswer={answer} />)}
    </div>
  )
}

// PersonItemTray is the dock's way to the same cards: an amber button with the count that opens
// them, so a plan is answerable without finding the agent first. It pulses until it is opened.
export function PersonItemTray() {
  const { t } = useTranslation()
  const { items, answer, openAgent } = usePersonItemsContext()
  const list = items.filter(isPlan)
  const [open, setOpen] = useState(false)
  const [seenCount, setSeenCount] = useState(0)
  const root = useRef(null)
  const btn = useRef(null)
  useEffect(() => {
    if (!open) return undefined
    const onDoc = e => { if (root.current && !root.current.contains(e.target)) setOpen(false) }
    document.addEventListener('mousedown', onDoc)
    return () => document.removeEventListener('mousedown', onDoc)
  }, [open])
  useEffect(() => { if (list.length === 0) setOpen(false) }, [list.length])
  if (list.length === 0) return null
  const fresh = !open && list.length > seenCount
  return (
    <div className="pi-tray" ref={root} data-testid="person-tray"
      onKeyDown={e => { if (e.key === 'Escape' && open) { e.stopPropagation(); setOpen(false); btn.current?.focus() } }}>
      <button type="button" ref={btn} className="pi-tray-btn" data-fresh={fresh} aria-expanded={open} aria-haspopup="true"
        aria-label={t('personItem.trayLabel', { count: list.length })} title={t('personItem.trayLabel', { count: list.length })}
        onClick={() => { setOpen(o => !o); setSeenCount(list.length) }}>
        <BellRing size={16} aria-hidden="true" />
        <span className="pi-tray-count" aria-hidden="true">{list.length > 9 ? '9+' : list.length}</span>
      </button>
      {open && (
        <div className="pi-tray-pop" role="region" aria-label={t('personItem.regionLabel')}>
          {list.map(it => <PersonItemCard key={it.id} item={it} onAnswer={answer} onOpenAgent={openAgent} />)}
        </div>
      )}
    </div>
  )
}

// PersonItemsInbox is the section at the top of the inbox (the Human in Loop panel): every open
// item, a plan as its full card, any other kind as a row that opens the chat it was raised in.
export function PersonItemsInbox() {
  const { t } = useTranslation()
  const { items, answer, openAgent } = usePersonItemsContext()
  if (items.length === 0) return null
  return (
    <section className="pi-inbox" data-testid="person-inbox" aria-label={t('personItem.inboxLabel')}>
      <div className="pi-inbox-head">
        <BellRing size={12} aria-hidden="true" />
        <span>{t('personItem.inboxTitle', { count: items.length })}</span>
        <MuteToggle />
      </div>
      {items.map(it => isPlan(it)
        ? <PersonItemCard key={it.id} item={it} onAnswer={answer} onOpenAgent={openAgent} showMute={false} />
        : (
          <div className="pi-row" key={it.id} data-kind={it.kind}>
            <span className="pi-row-text">{t('personItem.otherKind', { kind: it.kind })}</span>
            {openAgent && (it.conversationId || it.leadConversationId) && (
              <button type="button" className="btn btn-ghost btn-sm" onClick={() => openAgent({ ...it, conversationId: it.conversationId || it.leadConversationId })}>
                <ExternalLink size={11} aria-hidden="true" /> {t('personItem.openAgent')}
              </button>
            )}
          </div>
        ))}
    </section>
  )
}
