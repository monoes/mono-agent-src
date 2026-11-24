// Role inspector sections for automations (plan §7.4, §8 layer 8):
//   - Automations: this role's grants, with open-workflow and remove.
//   - Effective tools: read-only, exactly what the model sees (org tools,
//     granted tools, built-ins) from `org effective-tools`.
//   - A loud warning when a role that holds grants can still run Bash.
// Automation (endpoint) roles get a summary of their workflow instead.
import { useEffect, useState } from 'react'
import { ExternalLink, Trash2, Pencil, AlertTriangle, Workflow, WifiOff } from 'lucide-react'
import { api, notify } from '../../services/api.js'
import { confirm } from '../ConfirmDialog.jsx'
import i18n from '../../i18n.js'
import { Chip, TierChip, mono, mutedText } from '../orgs/ui.jsx'

const SOURCE_COLORS = { org: 'var(--text-muted)', grant: 'var(--cyan, #00b4d8)', builtin: 'var(--text-dim, #6c7b90)' }

export function EffectiveTools({ orgName, roleId, refreshKey }) {
  const [tools, setTools] = useState(null)
  const [error, setError] = useState('')

  useEffect(() => {
    if (!orgName || !roleId) return
    let cancelled = false
    setTools(null)
    setError('')
    api.getEffectiveTools(orgName, roleId).then(res => {
      if (cancelled) return
      if (!res || res.error) { setError(res?.error || 'Could not load tools.'); setTools([]); return }
      setTools(Array.isArray(res.tools) ? res.tools : [])
    })
    return () => { cancelled = true }
  }, [orgName, roleId, refreshKey])

  return (
    <section>
      <div className="form-label">Effective tools</div>
      {tools === null && <div style={mutedText}>Loading…</div>}
      {error && <div style={{ ...mono, fontSize: 10.5, color: '#f87171' }}>{error}</div>}
      {tools && !error && tools.length === 0 && <div style={mutedText}>No tools.</div>}
      {tools && tools.length > 0 && (
        <ul aria-label="Effective tools" style={{ listStyle: 'none', margin: 0, padding: 0, display: 'flex', flexDirection: 'column', gap: 3 }}>
          {tools.map(t => (
            <li key={`${t.source}:${t.name}`} title={t.description || ''} style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
              <span style={{ ...mono, fontSize: 10.5, color: 'var(--text-secondary)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', flex: 1 }}>{t.name}</span>
              <Chip color={SOURCE_COLORS[t.source] || 'var(--text-muted)'}>{t.source}</Chip>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

export function BashBypassWarning({ roleTitle, onDenyBash }) {
  return (
    <div role="alert" style={{ display: 'flex', flexDirection: 'column', gap: 6, padding: '8px 10px', border: '1.5px solid var(--red, #ef4444)', borderRadius: 6, background: 'rgba(239,68,68,0.12)' }}>
      <div style={{ ...mono, fontSize: 11, fontWeight: 700, color: 'var(--red, #ef4444)', display: 'flex', alignItems: 'center', gap: 6 }}>
        <AlertTriangle size={13} /> This role can bypass its grants
      </div>
      <div style={{ ...mono, fontSize: 10.5, color: 'var(--text-secondary)' }}>
        {roleTitle} holds automation grants but Bash is not denied. With Bash it can run monoagentcli with your full rights.
      </div>
      {onDenyBash && (
        <button className="btn btn-danger btn-sm" onClick={onDenyBash} style={{ alignSelf: 'flex-start' }}>Deny Bash</button>
      )}
    </div>
  )
}

export default function RoleAutomationsSection({
  orgName, node, grants = [], automations = [], engineOffline = false,
  onChanged, onOpenWorkflow, onEditGrant, onDenyBash, bashDenied,
}) {
  const [busy, setBusy] = useState(null)
  if (!node) return null
  const isEndpoint = node.rest?.kind === 'endpoint'

  if (isEndpoint) {
    const auto = node.rest?.automation || {}
    const member = automations.find(a => a.alias === auto.alias || a.workflow_id === auto.workflow_id)
    return (
      <section>
        <div className="form-label">Automation role</div>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
            <Workflow size={12} style={{ color: '#a78bfa' }} />
            <span style={{ ...mono, fontSize: 11, color: 'var(--text)', flex: 1 }}>{member?.workflow_name || auto.alias || auto.workflow_id || 'workflow'}</span>
            {auto.workflow_id && (
              <button className="btn btn-sm btn-ghost" onClick={() => onOpenWorkflow?.(auto.workflow_id)} aria-label="Open workflow"><ExternalLink size={11} /></button>
            )}
          </div>
          <div style={mutedText}>Receives messages like any role and replies with its {auto.reply === 'last_node' || !auto.reply ? 'last node output' : auto.reply.replace(/^node:/, 'output of ')}.</div>
          {node.rest?.endpoint?.input_hint && <div style={mutedText}>Input: {node.rest.endpoint.input_hint}</div>}
          {engineOffline && (
            <div role="alert" style={{ ...mono, fontSize: 10.5, color: '#eab308', display: 'flex', alignItems: 'center', gap: 6 }}>
              <WifiOff size={11} /> Engine offline: messages queue until monoagentcli daemon runs.
            </div>
          )}
        </div>
      </section>
    )
  }

  const mine = grants.filter(g => g.role === node.id)

  const remove = async (g) => {
    const ok = await confirm(i18n.t('orgConfirm.revokeGrantBody', { role: node.title || node.id, alias: g.alias }), { title: i18n.t('orgConfirm.revokeGrantTitle'), confirmLabel: i18n.t('orgConfirm.revokeGrantConfirm'), danger: true })
    if (!ok) return
    setBusy(g.alias)
    try {
      const res = await api.removeOrgGrant(orgName, node.id, g.alias)
      if (!res || res.error) notify('revoke grant', res?.error || 'failed to revoke grant')
      await onChanged?.()
    } finally {
      setBusy(null)
    }
  }

  return (
    <>
      {mine.length > 0 && !bashDenied && (
        <BashBypassWarning roleTitle={node.title || node.id} onDenyBash={onDenyBash} />
      )}
      <section>
        <div className="form-label">Automations</div>
        {mine.length === 0 ? (
          <div style={mutedText}>No grants. Drag an automation onto this role to grant it.</div>
        ) : (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
            {mine.map(g => {
              const member = automations.find(a => a.alias === g.alias)
              return (
                <div key={g.alias} data-testid="role-grant" style={{ display: 'flex', flexDirection: 'column', gap: 4, padding: '6px 8px', border: '1px solid var(--border)', borderRadius: 6 }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
                    <span style={{ ...mono, fontSize: 11, color: 'var(--text)', flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{g.workflow_name || member?.workflow_name || g.alias}</span>
                    <button className="btn btn-sm btn-ghost" style={{ padding: '2px 4px' }} onClick={() => onOpenWorkflow?.(g.workflow_id || member?.workflow_id)} aria-label={`Open workflow ${g.alias}`}><ExternalLink size={10} /></button>
                    {onEditGrant && member && (
                      <button className="btn btn-sm btn-ghost" style={{ padding: '2px 4px' }} onClick={() => onEditGrant(node, member, g)} aria-label={`Edit grant ${g.alias}`}><Pencil size={10} /></button>
                    )}
                    <button className="btn btn-sm btn-ghost" style={{ padding: '2px 4px' }} disabled={busy === g.alias} onClick={() => remove(g)} aria-label={`Revoke ${g.alias}`}><Trash2 size={10} /></button>
                  </div>
                  <div style={{ display: 'flex', gap: 4, flexWrap: 'wrap', alignItems: 'center' }}>
                    <Chip>{g.alias}</Chip>
                    <Chip>{g.mode || 'run'}</Chip>
                    {g.approval === 'required' ? <><Chip color="#eab308">needs a decision</Chip><TierChip tier={g.tier} /></> : <Chip>no decision</Chip>}
                    {g.max_calls_per_run != null && <span style={mutedText}>≤{g.max_calls_per_run}/run</span>}
                  </div>
                </div>
              )
            })}
          </div>
        )}
      </section>
    </>
  )
}
