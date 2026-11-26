import { useCallback, useEffect, useRef, useState } from 'react'
import { GetVaultAgentAccess, SetVaultAgentAccess } from '../wailsjs/go/main/App'
import { useTranslation } from 'react-i18next'
import { confirm } from './ConfirmDialog.jsx'
import { useReloadOnActivate } from '../lib/useReloadOnActivate'

const levels = [
  { value: 'strict', label: 'ui6Set.access_strict', description: 'ui6Set.access_strict_desc' },
  { value: 'use', label: 'ui6Set.access_use', description: 'ui6Set.access_use_desc' },
  { value: 'reveal', label: 'ui6Set.access_reveal', description: 'ui6Set.access_reveal_desc' },
]

export default function VaultAgentAccess({ isActive = true }) {
  const { t } = useTranslation()
  const [policy, setPolicy] = useState(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState(null) // { key, error }: translated when shown
  const pending = useRef(false)
  const generation = useRef(0)

  const load = useCallback(async () => {
    if (pending.current) return
    const request = ++generation.current
    setBusy(true)
    setError(null)
    try {
      const next = await GetVaultAgentAccess()
      if (request === generation.current) setPolicy(next)
    } catch (e) {
      if (request === generation.current) setError({ key: 'ui6Set.access_load_failed', error: String(e) })
    } finally {
      if (request === generation.current) setBusy(false)
    }
  }, [])

  useEffect(() => {
    load()
    return () => { generation.current++ }
  }, [load])
  useReloadOnActivate(isActive, load)

  const change = async (level) => {
    if (pending.current || busy || !policy || level === policy.level) return
    pending.current = true
    setBusy(true)
    setError(null)
    // Invalidate an earlier load so it cannot replace the saved policy.
    const request = ++generation.current
    try {
      if (level !== 'strict') {
        const message = level === 'reveal' ? t('vaultAccess.confirmReveal') : t('vaultAccess.confirmUse')
        if (!(await confirm(message, { title: t('vaultAccess.confirmTitle'), confirmLabel: t('vaultAccess.confirmAllow') }))) return
      }
      const next = await SetVaultAgentAccess(level)
      if (request === generation.current) setPolicy(next)
    } catch (e) {
      if (request === generation.current) setError({ key: 'ui6Set.access_change_failed', error: String(e) })
    } finally {
      pending.current = false
      if (request === generation.current) setBusy(false)
    }
  }

  const scoped = policy && (policy.secrets?.length > 0 || policy.hosts?.length > 0 || policy.expires_at)
  const current = levels.find(level => level.value === policy?.level)
  return (
    <section aria-label={t('ui6Set.access_section')} style={{ margin: '12px 20px 0', padding: 12, border: '1px solid var(--border, #1e3a4f)', borderRadius: 6, fontSize: 12 }}>
      <label htmlFor="vault-agent-access" style={{ display: 'block', fontWeight: 600, marginBottom: 6 }}>{t('ui6Set.access_label')}</label>
      <select id="vault-agent-access" value={policy?.level || ''} disabled={busy || !policy} onChange={e => change(e.target.value)} aria-describedby="vault-agent-access-description"
        style={{ maxWidth: '100%' }}>
        {!policy && <option value="">{busy ? t('ui6Set.loading') : t('ui6Set.unavailable')}</option>}
        {levels.map(level => <option key={level.value} value={level.value}>{t(level.label)}</option>)}
      </select>
      <p id="vault-agent-access-description" style={{ margin: '7px 0 0', color: 'var(--text-muted, #94a3b8)', lineHeight: 1.5 }}>
        {current && t(current.description)}
        {policy && ` ${t('ui6Set.access_encrypted')}`}
        {policy?.level === 'use' && ` ${t('ui6Set.access_masking')}`}
      </p>
      {scoped && <p style={{ margin: '6px 0 0', color: 'var(--text-muted, #94a3b8)' }}>
        {t('ui6Set.access_scope', { secrets: policy.secrets?.length ? policy.secrets.join(', ') : t('ui6Set.access_all_secrets') })}
        {policy.hosts?.length > 0 && ` ${t('ui6Set.access_hosts', { hosts: policy.hosts.join(', ') })}`}
        {policy.expires_at && ` ${t('ui6Set.access_expires', { date: new Date(policy.expires_at).toLocaleString() })}`}
      </p>}
      {error && <div role="alert" style={{ marginTop: 7, color: '#fca5a5' }}>{t(error.key, { error: error.error })} <button onClick={load} disabled={busy}>{t('ui6Set.retry')}</button></div>}
    </section>
  )
}
