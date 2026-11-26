import { useCallback, useEffect, useRef, useState } from 'react'
import { ShieldAlert, ShieldOff, ShieldQuestion, Clock } from 'lucide-react'
import { api } from '../../services/api.js'
import { confirm } from '../ConfirmDialog.jsx'
import { cachedAgentScan } from '../../lib/agentRuntimes.js'
import { useTranslation } from 'react-i18next'
import i18n from '../../i18n.js'

// Full-access org roles (#205). A role with policy.access "full" runs like a
// coder chat: any command, any file, no approval gate. Its state comes from
// `org status`'s roles_access ([{role, access, access_state, reason?}],
// absent when no role declares full access):
//   active             — the grant holds
//   suspended          — the role's config changed since the grant; it runs
//                        scoped until a human grants it again
//   unattended-blocked — scheduled/unattended runs don't use it, because
//                        the org doesn't allow unattended full access
// Granting is human-only: only after the confirm dialog below, and the CLI
// refuses a grant from an agent context.

// The canvas card shows only "FULL": its icon, color and tooltip carry the
// state.
export const ACCESS_STATES = {
  active: { get label() { return i18n.t('ui6OrgsMore.state_active') }, color: '#f59e0b', icon: ShieldAlert },
  suspended: { get label() { return i18n.t('ui6OrgsMore.state_suspended') }, color: '#ef4444', icon: ShieldOff },
  'unattended-blocked': { get label() { return i18n.t('ui6OrgsMore.state_unattended') }, color: '#fb923c', icon: Clock },
  // Not a monomind state: the role declares full access but no human grant
  // is on file (from `org validate`'s output; see withNotGranted).
  'not-granted': { get label() { return i18n.t('ui6OrgsMore.state_not_granted') }, color: '#94a3b8', icon: ShieldQuestion },
}
const UNKNOWN_STATE = { get label() { return i18n.t('ui6OrgsMore.state_active') }, color: '#f59e0b', icon: ShieldAlert }

export function accessState(entry) {
  return ACCESS_STATES[entry?.access_state] || UNKNOWN_STATE
}

// A role that was never granted comes back "suspended" with this reason
// (monomind 2.18); it is shown as not granted, since there is nothing to
// grant again.
const NEVER_GRANTED = /no human acknowledgement on file/

// rolesAccessByRole maps role id → roles_access entry.
export function rolesAccessByRole(status) {
  const out = {}
  for (const e of Array.isArray(status?.roles_access) ? status.roles_access : []) {
    if (!e?.role) continue
    out[e.role] = e.access_state === 'suspended' && NEVER_GRANTED.test(e.reason || '')
      ? { ...e, access_state: 'not-granted' }
      : e
  }
  return out
}

// withNotGranted adds a "not-granted" entry for each role that `org
// validate` says has no human acknowledgement and that roles_access doesn't
// cover. Before monomind 2.18 an org that never ran, or is stopped,
// reported no roles_access at all (monomind#367); this keeps that case
// working.
export function withNotGranted(byRole, unacknowledged = {}) {
  const out = { ...byRole }
  for (const [role, line] of Object.entries(unacknowledged)) {
    if (!out[role]) out[role] = { role, access: 'full', access_state: 'not-granted', reason: line }
  }
  return out
}

// declaresFull reports whether a role's saved policy asks for full access.
export function declaresFull(node) {
  return node?.rest?.policy?.access === 'full'
}

// runtimeFullAccess reports whether monomind can run a runtime with full
// access: its `agent scan` entry's full_access. A scan from before that
// field (or none at all) leaves claude as the only full-access runtime.
export function runtimeFullAccess(scan, runtimeID) {
  const entry = (Array.isArray(scan?.agents) ? scan.agents : []).find(a => a.id === runtimeID)
  if (typeof entry?.full_access === 'boolean') return entry.full_access
  return runtimeID === 'claude'
}

// useRuntimeFullAccess is runtimeFullAccess over the shared (cached) scan;
// null until the scan answers.
export function useRuntimeFullAccess(runtimeID) {
  const [scan, setScan] = useState(undefined)
  useEffect(() => {
    let current = true
    Promise.resolve().then(() => cachedAgentScan()).catch(() => null)
      .then(res => { if (current) setScan(res && !res.error ? res : null) })
    return () => { current = false }
  }, [])
  return scan === undefined ? null : runtimeFullAccess(scan, runtimeID)
}

// useRolesAccess loads the org's roles_access and reloads it whenever
// `stamp` changes (the designer passes its role config, so an edit that
// suspends a grant shows up), debounced so a burst of edits costs one call.
export function useRolesAccess(orgName, stamp = '') {
  const [byRole, setByRole] = useState({})
  const current = useRef(orgName)
  current.current = orgName
  const refresh = useCallback(async () => {
    const name = orgName
    if (!name) { setByRole({}); return }
    const status = await Promise.resolve().then(() => api.getOrgStatus(name)).catch(() => null)
    if (current.current === name) setByRole(rolesAccessByRole(status))
  }, [orgName])
  useEffect(() => {
    const id = setTimeout(refresh, stamp ? 600 : 0)
    return () => clearTimeout(id)
  }, [refresh, stamp])
  return { byRole, refresh }
}

export function FullAccessBadge({ entry, compact = false }) {
  const { t } = useTranslation()
  const st = accessState(entry)
  const Icon = st.icon
  const title = entry?.reason ? `${st.label}: ${entry.reason}` : st.label
  return (
    <span data-testid="full-access-badge" data-state={entry?.access_state || 'unknown'} title={title} style={{
      display: 'inline-flex', alignItems: 'center', gap: 3, flexShrink: 0,
      fontFamily: 'var(--font-mono)', fontSize: compact ? 7.5 : 8.5, fontWeight: 700, letterSpacing: 0.8,
      color: st.color, background: `${st.color}14`, border: `1px solid ${st.color}66`,
      borderRadius: 4, padding: compact ? '0 4px' : '1px 5px', whiteSpace: 'nowrap',
    }}>
      <Icon size={compact ? 8 : 9} /> {compact ? t('ui6OrgsMore.badge_full') : st.label.toUpperCase()}
    </span>
  )
}

export const FULL_ACCESS_HELP = 'monomind org role set-access --help'

// The grant dialog's body: coder mode's risks plus the org specifics.
export function FullAccessRiskText({ role }) {
  const { t } = useTranslation()
  return (
    <div data-testid="full-access-risk-text">
      <p style={{ margin: '0 0 8px' }}>{t('ui6OrgsMore.risk_role')} <strong>{role}</strong> {t('ui6OrgsMore.risk_will_work')}</p>
      <ul style={{ margin: 0, paddingLeft: 18, display: 'flex', flexDirection: 'column', gap: 6 }}>
        <li>{t('ui6OrgsMore.risk_commands')}</li>
        <li>{t('ui6OrgsMore.risk_untrusted')}</li>
        <li>{t('ui6OrgsMore.risk_unattended')}</li>
        <li>{t('ui6OrgsMore.risk_setup')}</li>
      </ul>
      <p style={{ margin: '8px 0 0', fontSize: 11, color: 'var(--text-muted)' }}>
        {t('ui6OrgsMore.risk_suspends')} <code>{FULL_ACCESS_HELP}</code>.
      </p>
    </div>
  )
}

// grantFullAccess asks for the confirmation, then grants. Resolves the CLI's
// result, or null when the user cancelled; rejects with the CLI's refusal.
export async function grantFullAccess(orgName, roleID, { again = false } = {}) {
  const ok = await confirm(<FullAccessRiskText role={roleID} />, {
    title: again ? i18n.t('ui6OrgsMore.grant_again_title', { role: roleID }) : i18n.t('ui6OrgsMore.grant_title', { role: roleID }),
    confirmLabel: again ? i18n.t('ui6OrgsMore.grant_again') : i18n.t('ui6OrgsMore.grant_full'),
    danger: true,
  })
  if (!ok) return null
  return api.orgRoleSetAccess(orgName, roleID, 'full')
}

export function revokeFullAccess(orgName, roleID) {
  return api.orgRoleSetAccess(orgName, roleID, 'scoped')
}
