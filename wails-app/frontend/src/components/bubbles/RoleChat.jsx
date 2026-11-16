import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ArrowLeft } from 'lucide-react'
import { useCoderConversation } from './useCoderConversation.js'
import { CoderChatView } from './CoderChatView.jsx'

// RoleChat is one role's conversation (the boss, a planner, an executor) in the chat area of the run's overlay: the run's
// roster stays above it. Typing to the boss is steering of the run (steerChat is the chat the boss stands behind); the other
// roles are read and answered as plain lite chats. Keyed by the role's conversation by its caller, so each owns its own state.
export function RoleChat({ role, frontDesk, access, setup, runtime, onBack, onNavigate, onOpenArtifact }) {
  const { t } = useTranslation()
  const conv = useCoderConversation({ conversationId: role.conversationId, access })
  const [draft, setDraft] = useState('')
  return (
    <div style={{ flex: 1, minHeight: 0, display: 'flex', flexDirection: 'column' }} data-testid="role-chat">
      <button type="button" className="btn btn-ghost btn-sm" onClick={onBack} style={{ alignSelf: 'flex-start', gap: 4, margin: '4px 8px' }}>
        <ArrowLeft size={11} aria-hidden="true" /> {t('bubbles.roster.back')}
      </button>
      <CoderChatView conv={conv} isDraft={false} access={access} setup={setup} draft={draft} onDraftChange={setDraft}
        onNavigate={onNavigate} onOpenArtifact={onOpenArtifact} runtime={runtime} variant="lite"
        steerChat={role.boss ? frontDesk : ''} directBoss={!!role.boss} />
    </div>
  )
}
