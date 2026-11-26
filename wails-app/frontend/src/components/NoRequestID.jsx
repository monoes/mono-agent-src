// An approval request without an id (a monomind older than request-scoped
// approvals) can only be resolved together with every other call of the same
// role and tool, one queued after the card was drawn included. The app
// resolves only what it showed, so it offers no buttons for it.
export function NoRequestID({ org, role, action }) {
  return (
    <div role="note" style={{ fontFamily: 'var(--font-mono)', fontSize: 10.5, color: '#fbbf24', overflowWrap: 'anywhere' }}>
      This request has no request id, so the app cannot resolve it on its own. Update monomind, or run{' '}
      <code>monoagentcli --profile &lt;profile&gt; org approve|deny {org} {role} {action}</code> in a terminal,
      which resolves every waiting {action} call of {role}.
    </div>
  )
}
