import { useState } from 'react'
import { FullAccessBadge, accessState, grantFullAccess, revokeFullAccess, useRuntimeFullAccess } from './fullAccess.jsx'
import { useTranslation } from 'react-i18next'
import i18n from '../../i18n.js'

// The role editor's "Full access" control (#205): grant (after the risk
// confirmation), grant again for a suspended role, and revoke. The grant
// state is `org status`'s roles_access entry for this role; declared says
// the saved policy asks for full access even if no state came back.
// runtime is the role's effective runtime (its own, else the org's): only
// a runtime monomind can run with full access (agent scan's full_access)
// can be granted; revoking stays possible either way.

const mono = 'var(--font-mono)'
const hint = { fontFamily: 'var(--font-body)', fontSize: 10.5, color: 'var(--text-muted)', lineHeight: 1.5 }

const STATE_HINT = {
  active: 'ui6OrgsMore.hint_active',
  suspended: 'ui6OrgsMore.hint_suspended',
  'unattended-blocked': 'ui6OrgsMore.hint_unattended',
  'not-granted': 'ui6OrgsMore.hint_not_granted',
}

export default function RoleFullAccessSection({ orgName, roleID, entry, declared, runtime = 'claude', onChanged }) {
  const { t } = useTranslation()
  const capable = useRuntimeFullAccess(runtime)
  const [busy, setBusy] = useState('')
  const [err, setErr] = useState('')
  const [note, setNote] = useState('')
  const isFull = !!entry || declared
  const state = entry?.access_state

  const run = async (what, fn) => {
    setBusy(what); setErr(''); setNote('')
    try {
      const res = await fn()
      if (res) { setNote(res.message || (what === 'revoke' ? i18n.t('ui6OrgsMore.revoked_note') : i18n.t('ui6OrgsMore.granted_note'))); onChanged?.() }
    } catch (e) {
      setErr(String(e?.message || e))
    } finally {
      setBusy('')
    }
  }
  const grant = (again) => run('grant', () => grantFullAccess(orgName, roleID, { again }))
  const revoke = () => run('revoke', () => revokeFullAccess(orgName, roleID))

  // null while the scan is loading: grant stays off until it answers.
  const canGrant = capable === true
  const runtimeNote = capable === false && (
    <div data-testid="full-access-runtime-note" style={hint}>
      {t('ui6OrgsMore.runtime_cannot', { runtime })}
    </div>
  )

  return (
    <section data-testid="role-full-access">
      <div className="form-label" style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
        {t('ui6OrgsMore.full_access')} {isFull && <FullAccessBadge entry={entry} />}
      </div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
        {isFull ? (
          <>
            <div style={hint}>{t(STATE_HINT[state] || 'ui6OrgsMore.hint_declares')}</div>
            {entry?.reason && state !== 'active' && (
              <div data-testid="full-access-reason" style={{ fontFamily: mono, fontSize: 10, color: accessState(entry).color, lineHeight: 1.5, wordBreak: 'break-word' }}>
                {entry.reason}
              </div>
            )}
            {runtimeNote}
            <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
              {state === 'suspended' && (
                <button type="button" className="btn btn-primary btn-sm" disabled={!!busy || !canGrant} onClick={() => grant(true)}>
                  {busy === 'grant' ? t('ui6OrgsMore.granting') : t('ui6OrgsMore.grant_again_more')}
                </button>
              )}
              {state === 'not-granted' && (
                <button type="button" className="btn btn-primary btn-sm" disabled={!!busy || !canGrant} onClick={() => grant(false)}>
                  {busy === 'grant' ? t('ui6OrgsMore.granting') : t('ui6OrgsMore.grant_full_more')}
                </button>
              )}
              <button type="button" className="btn btn-secondary btn-sm" disabled={!!busy} onClick={revoke}>
                {busy === 'revoke' ? t('ui6OrgsMore.revoking') : t('ui6OrgsMore.revoke')}
              </button>
            </div>
          </>
        ) : (
          <>
            <div style={hint}>
              {t('ui6OrgsMore.full_access_explain')}
            </div>
            {runtimeNote}
            <div>
              <button type="button" className="btn btn-secondary btn-sm" disabled={!!busy || !orgName || !canGrant} onClick={() => grant(false)}>
                {busy === 'grant' ? t('ui6OrgsMore.granting') : t('ui6OrgsMore.grant_full_more')}
              </button>
            </div>
          </>
        )}
        {note && <div style={{ fontFamily: mono, fontSize: 10, color: 'var(--green-neon)', wordBreak: 'break-word' }}>{note}</div>}
        {err && <div role="alert" style={{ fontFamily: mono, fontSize: 10, color: 'var(--red)', lineHeight: 1.5, wordBreak: 'break-word' }}>{err}</div>}
      </div>
    </section>
  )
}
