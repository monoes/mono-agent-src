import { useTranslation } from 'react-i18next'

// ChatResetBar is the always-visible Clear / Compact pair above a chat:
// Clear starts a fresh conversation, Compact summarizes this one and
// continues in a fresh one that opens with the summary.
export function ChatResetBar({ onClear, onCompact, clearDisabled, compactDisabled }) {
  const { t } = useTranslation()
  return (
    <div style={{ padding: '5px 12px', display: 'flex', gap: 6, borderBottom: '1px solid rgba(0,180,216,0.06)', flexShrink: 0 }}>
      <button type="button" className="btn btn-ghost btn-sm" onClick={onClear} disabled={clearDisabled}
        title={t('chat.clearTitle', 'Start a fresh conversation (history stays under past sessions)')} style={{ fontSize: 10 }}>
        {t('chat.clear', 'Clear')}
      </button>
      <button type="button" className="btn btn-ghost btn-sm" onClick={onCompact} disabled={compactDisabled}
        title={t('chat.compactTitle', 'Summarize this conversation and continue in a fresh one')} style={{ fontSize: 10 }}>
        {t('chat.compact', 'Compact')}
      </button>
    </div>
  )
}
