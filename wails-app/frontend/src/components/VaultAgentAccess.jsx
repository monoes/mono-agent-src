import { useCallback, useEffect, useRef, useState } from 'react'
import { GetVaultAgentAccess, SetVaultAgentAccess } from '../wailsjs/go/main/App'
import { confirm } from './ConfirmDialog.jsx'
import { useReloadOnActivate } from '../lib/useReloadOnActivate'

const levels = [
  { value: 'strict', label: '1. Strict', description: 'Agents cannot read or use vault passwords without your approval.' },
  { value: 'use', label: '2. Use without revealing', description: 'Agents can use passwords through supported tools and workflows without retrieving their plaintext values.' },
  { value: 'reveal', label: '3. Reveal', description: 'Agents can retrieve plaintext passwords as well as use them.' },
]

export default function VaultAgentAccess({ isActive = true }) {
  const [policy, setPolicy] = useState(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const pending = useRef(false)
  const generation = useRef(0)

  const load = useCallback(async () => {
    if (pending.current) return
    const request = ++generation.current
    setBusy(true)
    setError('')
    try {
      const next = await GetVaultAgentAccess()
      if (request === generation.current) setPolicy(next)
    } catch (e) {
      if (request === generation.current) setError('Could not load agent access: ' + String(e))
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
    setError('')
    // Invalidate an earlier load so it cannot replace the saved policy.
    const request = ++generation.current
    try {
      if (level !== 'strict') {
        const message = level === 'reveal'
          ? 'Allow all agents to retrieve plaintext passwords from this profile’s vault?'
          : 'Allow all agents to use this profile’s vault passwords through supported tools and workflows?'
        if (!(await confirm(message, { title: 'Change vault agent access', confirmLabel: 'Allow access' }))) return
      }
      const next = await SetVaultAgentAccess(level)
      if (request === generation.current) setPolicy(next)
    } catch (e) {
      if (request === generation.current) setError('Could not change agent access: ' + String(e))
    } finally {
      pending.current = false
      if (request === generation.current) setBusy(false)
    }
  }

  const scoped = policy && (policy.secrets?.length > 0 || policy.hosts?.length > 0 || policy.expires_at)
  return (
    <section aria-label="Vault agent access" style={{ margin: '12px 20px 0', padding: 12, border: '1px solid var(--border, #1e3a4f)', borderRadius: 6, fontSize: 12 }}>
      <label htmlFor="vault-agent-access" style={{ display: 'block', fontWeight: 600, marginBottom: 6 }}>Agent access · current profile</label>
      <select id="vault-agent-access" value={policy?.level || ''} disabled={busy || !policy} onChange={e => change(e.target.value)} aria-describedby="vault-agent-access-description"
        style={{ maxWidth: '100%' }}>
        {!policy && <option value="">{busy ? 'Loading…' : 'Unavailable'}</option>}
        {levels.map(level => <option key={level.value} value={level.value}>{level.label}</option>)}
      </select>
      <p id="vault-agent-access-description" style={{ margin: '7px 0 0', color: 'var(--text-muted, #94a3b8)', lineHeight: 1.5 }}>
        {levels.find(level => level.value === policy?.level)?.description}
        {policy && ' Passwords remain encrypted on disk.'}
        {policy?.level === 'use' && ' Output masking is best-effort; enable only for agents you trust.'}
      </p>
      {scoped && <p style={{ margin: '6px 0 0', color: 'var(--text-muted, #94a3b8)' }}>
        Current scope: {policy.secrets?.length ? policy.secrets.join(', ') : 'all secrets'}.
        {policy.hosts?.length > 0 && ` Hosts: ${policy.hosts.join(', ')}.`}
        {policy.expires_at && ` Expires: ${new Date(policy.expires_at).toLocaleString()}.`}
      </p>}
      {error && <div role="alert" style={{ marginTop: 7, color: '#fca5a5' }}>{error} <button onClick={load} disabled={busy}>Retry</button></div>}
    </section>
  )
}
