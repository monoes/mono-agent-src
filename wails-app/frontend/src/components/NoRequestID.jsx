import { useTranslation } from 'react-i18next'
import t from '../lib/i18nOr.js'

// The names come from monomind: an odd one is left out of the command a person
// might paste into a shell.
const word = (value, placeholder) => (/^[A-Za-z0-9][A-Za-z0-9_.:-]*$/.test(value || '') ? value : placeholder)

// An approval request without an id (a monomind older than request-scoped
// approvals) can only be resolved together with every other call of the same
// role and tool, one queued after the card was drawn included. The app never
// approves what it has not shown, so it offers no Approve for it; with
// canDeny the card keeps its Deny, and only approving is explained.
export function NoRequestID({ org, role, action, canDeny = false }) {
  useTranslation() // renders again on a language change
  return (
    <div role="note" style={{ fontFamily: 'var(--font-mono)', fontSize: 10.5, color: '#fbbf24', overflowWrap: 'anywhere' }}>
      {t(canDeny ? 'ui7Hil.noid_approve_before' : 'ui7Hil.noid_before')}{' '}
      <code>monoagentcli --profile &lt;profile&gt; org {canDeny ? 'approve' : 'approve|deny'} {word(org, '<org>')} {word(role, '<role>')} {word(action, '<action>')}</code>{' '}
      {t(canDeny ? 'ui7Hil.noid_approve_after' : 'ui7Hil.noid_after', { action: word(action, '<action>'), role: word(role, '<role>') })}
    </div>
  )
}
