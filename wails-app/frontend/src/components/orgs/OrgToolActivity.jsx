import { Terminal } from 'lucide-react'
import { ChatTimeline } from '../chat/ChatTimeline.jsx'
import { buildToolActivity } from './orgToolActivity.js'
import { useTranslation } from 'react-i18next'

// The tool calls full-access roles made on this bus (#205 item 6), grouped
// per role and drawn with the coder chat's cards: the audit view of what a
// role with full access actually ran and changed. Renders nothing when the
// events hold no tool_activity. isLive keeps a call without its end
// spinning; a finished run shows it as interrupted instead.
export default function OrgToolActivity({ events, isLive = false, runKey = '' }) {
  const { t } = useTranslation()
  const groups = buildToolActivity(events)
  if (groups.length === 0) return null
  return (
    <div data-testid="org-tool-activity" style={{ display: 'flex', flexDirection: 'column', gap: 10, marginBottom: 12 }}>
      {groups.map(({ role, state }) => (
        <div key={role} data-testid="org-tool-activity-role" data-role={role}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 6, fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: 1 }}>
            <Terminal size={11} color="#f59e0b" />
            <span style={{ color: '#f59e0b' }}>{role === 'unknown role' ? t('ui6Orgs.unknown_role') : role}</span>
            <span>{t('ui6Orgs.tool_calls', { count: Object.keys(state.calls).length })}</span>
          </div>
          <div style={{ display: 'flex', flexDirection: 'column' }}>
            <ChatTimeline state={state} turnId={`org-${runKey}-${role}`} isLive={isLive} />
          </div>
        </div>
      ))}
    </div>
  )
}
