import { useTranslation } from 'react-i18next'
import t from '../lib/i18nOr.js'

// An approval request without an id (a monomind older than request-scoped
// approvals) can only be resolved together with every other call of the same
// role and tool, one queued after the card was drawn included. The app
// resolves only what it showed, so it offers no buttons for it.
export function NoRequestID({ org, role, action }) {
  useTranslation() // renders again on a language change
  return (
    <div role="note" style={{ fontFamily: 'var(--font-mono)', fontSize: 10.5, color: '#fbbf24', overflowWrap: 'anywhere' }}>
      {t('ui7Hil.noid_before')}{' '}
      <code>monoagentcli --profile &lt;profile&gt; org approve|deny {org} {role} {action}</code>{' '}
      {t('ui7Hil.noid_after', { action, role })}
    </div>
  )
}
