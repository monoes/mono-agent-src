import { useState, useEffect, useRef, useMemo, useCallback } from 'react'
import { useTranslation } from 'react-i18next'
import { Search, PartyPopper } from 'lucide-react'
import { useSendToCoder } from '../../components/bubbles/CoderBubbleContext.jsx'
import { TOPICS } from './helpContent.js'
import { runHelpAction } from './helpActions.js'
import HelpTopicCard from './HelpTopicCard.jsx'
import PermissionPit, { Mascot } from './PermissionPit.jsx'
import { clickQuip, shouldConfetti, createKonami, searchEgg, filterTopics } from './helpEasterEggs.js'
import './Help.css'

const CONFETTI = ['🎉', '✨', '🧯', '🔑', '🪄', '🎈', '🦆', '🍕']

export default function Help({ onNavigate }) {
  const { t } = useTranslation()
  const { openNewChat, openDraft } = useSendToCoder() || {}
  const [query, setQuery] = useState('')
  const [openId, setOpenId] = useState(null)
  const [pokes, setPokes] = useState(0)
  const [party, setParty] = useState(false)
  const [note, setNote] = useState('')
  const konami = useRef(null)
  if (!konami.current) konami.current = createKonami()

  useEffect(() => {
    const onKey = (e) => {
      if (konami.current(e.key)) { setParty(p => !p); setNote('help.egg.konami') }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  const topics = useMemo(() => filterTopics(TOPICS, query), [query])
  const eggKey = searchEgg(query)

  const onAction = useCallback(async (action) => {
    try {
      const r = await runHelpAction(action, { navigate: onNavigate, openNewChat, openDraft })
      // runHelpAction reports a missing coder/provider as { ok:false, ... } and never throws.
      if (r && r.ok === false) setNote('help.actionFailed')
    } catch {
      setNote('help.actionFailed')
    }
  }, [onNavigate, openNewChat, openDraft])

  const poke = () => {
    const n = pokes + 1
    setPokes(n)
    setNote(shouldConfetti(n) ? 'help.egg.confetti' : clickQuip(n))
  }
  const confetti = shouldConfetti(pokes)
  const noteText = note ? t(note) : t('help.egg.idle')

  return (
    <div className={`page-body help-page${party ? ' help-party' : ''}`} data-testid="help-page">
      <header className="help-hero">
        <Mascot depth={0} label={t('help.mascotLabel')} onPoke={poke} />
        <div className="help-hero-text">
          <h1 className="help-title">{t('help.title')}</h1>
          <p className="help-sub">{t('help.subtitle')}</p>
          <p className="help-bubble" role="status" aria-live="polite" data-testid="help-note">{noteText}</p>
        </div>
        {party && <span className="help-party-badge"><PartyPopper size={14} aria-hidden="true" /> {t('help.party')}</span>}
      </header>

      {confetti && (
        <div className="help-confetti" aria-hidden="true" data-testid="help-confetti">
          {CONFETTI.map((c, i) => <span key={i} style={{ '--x': `${(i - 3.5) * 40}px`, '--d': `${i * 60}ms` }}>{c}</span>)}
        </div>
      )}

      <div className="help-search">
        <Search size={14} aria-hidden="true" />
        <input
          type="search" value={query} onChange={e => setQuery(e.target.value)}
          placeholder={t('help.searchPlaceholder')} aria-label={t('help.searchLabel')}
        />
      </div>
      {eggKey && <p className="help-egg-reply" role="status">{t(eggKey)}</p>}

      <PermissionPit onAction={onAction} />

      <h2 className="section-title help-section">{t('help.topics')}</h2>
      {topics.length === 0 ? (
        <div className="empty-state" data-testid="help-empty">
          <div className="empty-state-title">{t('help.noMatch')}</div>
          <p className="empty-state-desc">{t('help.noMatchWhy')}</p>
          <button type="button" className="btn btn-primary btn-sm" onClick={() => setQuery('')}>{t('help.clear')}</button>
        </div>
      ) : (
        <div className="help-grid">
          {topics.map(topic => (
            <HelpTopicCard
              key={topic.id} topic={topic} open={openId === topic.id}
              onToggle={id => setOpenId(cur => (cur === id ? null : id))} onAction={onAction}
            />
          ))}
        </div>
      )}
    </div>
  )
}
