import { useTranslation } from 'react-i18next'
import { Crown } from 'lucide-react'
import { useSendToCoder } from '../bubbles/CoderBubbleContext.jsx'
import { showBossInAgents } from '../../lib/showBoss.js'

// A link to the boss of the open-ended run on the Agents page. navigate defaults to the app's own (CoderBubbleContext); without
// one there is nowhere to go and nothing renders. before runs first (a chat overlay collapses itself so the page is seen).
export default function ShowBossLink({ navigate, before, style }) {
  const { t } = useTranslation()
  const ctx = useSendToCoder()
  const go = navigate || ctx?.navigate
  if (typeof go !== 'function') return null
  return (
    <button type="button" className="btn btn-ghost btn-sm" data-testid="show-boss-link" style={{ display: 'inline-flex', alignItems: 'center', gap: 4, ...style }}
      onClick={() => { before?.(); showBossInAgents(go) }}>
      <Crown size={11} aria-hidden="true" /> {t('agents.boss.show')}
    </button>
  )
}
