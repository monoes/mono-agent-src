import { useTranslation } from 'react-i18next'
import t from '../../lib/i18nOr.js'

// ChatResetBar is the always-visible Clear / Compact pair above a chat:
// Clear starts a fresh conversation, Compact summarizes this one and
// continues in a fresh one that opens with the summary.
export function ChatResetBar({ onClear, onCompact, clearDisabled, compactDisabled }) {
  useTranslation() // renders again on a language change
  return (
    <div style={{ padding: '5px 12px', display: 'flex', gap: 6, borderBottom: '1px solid rgba(0,180,216,0.06)', flexShrink: 0 }}>
      <button type="button" className="btn btn-ghost btn-sm" onClick={onClear} disabled={clearDisabled}
        title={t('ui7Chat.clear_title')} style={{ fontSize: 10 }}>
        {t('ui7Chat.clear')}
      </button>
      <button type="button" className="btn btn-ghost btn-sm" onClick={onCompact} disabled={compactDisabled}
        title={t('ui7Chat.compact_title')} style={{ fontSize: 10 }}>
        {t('ui7Chat.compact')}
      </button>
    </div>
  )
}
