import { useEffect, useState } from 'react'
import { ShieldAlert } from 'lucide-react'
import { api } from '../../services/api.js'
import { FullAccessBadge, accessState, grantFullAccess, withNotGranted } from '../orgdesigner/fullAccess.jsx'
import { unacknowledgedRoles } from '../orgdesigner/orgValidateReport.js'
import { useTranslation } from 'react-i18next'

// The org overview's full-access roles (#205), from `org status`'s
// roles_access: how many roles run with full access, each one's state, the
// reason when it isn't active, and "Grant again" for a suspended one. A
// stopped or never-run org reports no roles_access (monomind#367), so roles
// that `org validate` says have no human grant are listed as not granted.
// Granting and revoking otherwise live in the role editor (Design tab).

const mono = 'var(--font-mono)'

export default function FullAccessSummary({ orgName, status, onChanged }) {
  const { t } = useTranslation()
  const [unacknowledged, setUnacknowledged] = useState({})
  const [busy, setBusy] = useState('')
  const [err, setErr] = useState('')
  useEffect(() => {
    let current = true
    if (!orgName) return
    Promise.resolve().then(() => api.validateOrgReport(orgName))
      .then(r => { if (current) setUnacknowledged(unacknowledgedRoles(r)) })
      .catch(() => { if (current) setUnacknowledged({}) })
    return () => { current = false }
  }, [orgName, status])
  const listed = {}
  for (const r of Array.isArray(status?.roles_access) ? status.roles_access : []) if (r?.role) listed[r.role] = r
  const roles = Object.values(withNotGranted(listed, unacknowledged))
  if (roles.length === 0) return null

  const grant = async (role, again) => {
    setBusy(role); setErr('')
    try {
      if (await grantFullAccess(orgName, role, { again })) onChanged?.()
    } catch (e) {
      setErr(String(e?.message || e))
    } finally {
      setBusy('')
    }
  }

  return (
    <div data-testid="full-access-summary" style={{
      background: 'rgba(245,158,11,0.04)', border: '1px solid rgba(245,158,11,0.25)', borderRadius: 'var(--radius)',
      padding: '8px 10px', display: 'flex', flexDirection: 'column', gap: 6,
    }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 6, fontFamily: mono, fontSize: 10, color: '#f59e0b', textTransform: 'uppercase', letterSpacing: 1 }}>
        <ShieldAlert size={11} /> {t('ui6Orgs.full_access_roles', { count: roles.length })}
      </div>
      {roles.map(r => (
        <div key={r.role} data-testid="full-access-row" style={{ display: 'flex', flexDirection: 'column', gap: 3 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
            <span style={{ fontFamily: mono, fontSize: 11, color: 'var(--text)' }}>{r.role}</span>
            <FullAccessBadge entry={r} />
            {(r.access_state === 'suspended' || r.access_state === 'not-granted') && (
              <button type="button" className="btn btn-secondary btn-sm" disabled={!!busy} onClick={() => grant(r.role, r.access_state === 'suspended')}>
                {busy === r.role ? t('ui6Orgs.granting') : r.access_state === 'suspended' ? t('ui6Orgs.grant_again') : t('ui6Orgs.grant_full')}
              </button>
            )}
          </div>
          {r.reason && r.access_state !== 'active' && (
            <div style={{ fontFamily: mono, fontSize: 10, color: accessState(r).color, lineHeight: 1.5, wordBreak: 'break-word' }}>{r.reason}</div>
          )}
        </div>
      ))}
      {err && <div role="alert" style={{ fontFamily: mono, fontSize: 10, color: 'var(--red)', lineHeight: 1.5, wordBreak: 'break-word' }}>{err}</div>}
    </div>
  )
}
