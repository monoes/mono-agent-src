// Grants matrix (plan §7.4): agent roles × org automations. Click an empty
// cell to grant with safe defaults (outbound workflows need a decision), a
// filled cell to revoke, or the pencil to open the full grant dialog.
import { useState } from 'react'
import { Check, Plus, Pencil, Send } from 'lucide-react'
import { api, notify } from '../../services/api.js'
import { confirm } from '../ConfirmDialog.jsx'
import i18n from '../../i18n.js'
import { defaultGrantSpec } from './GrantDialog.jsx'
import { TierChip, mono, mutedText } from '../orgs/ui.jsx'

export function isAutomationRole(node) {
  return node?.rest?.kind === 'endpoint'
}

const cellBtn = {
  display: 'inline-flex', alignItems: 'center', justifyContent: 'center', gap: 4,
  minWidth: 44, height: 24, borderRadius: 'var(--radius)', cursor: 'pointer',
  fontFamily: 'var(--font-mono)', fontSize: 10,
}

export default function GrantsMatrix({ orgName, nodes, automations, grants, onChanged, onEditGrant }) {
  const [busy, setBusy] = useState(null)
  const roles = (nodes || []).filter(n => !isAutomationRole(n))
  const byKey = new Map((grants || []).map(g => [`${g.role}|${g.alias}`, g]))

  const toggle = async (role, automation) => {
    const key = `${role.id}|${automation.alias}`
    const existing = byKey.get(key)
    if (existing) {
      const ok = await confirm(i18n.t('orgConfirm.revokeGrantBody', { role: role.title || role.id, alias: automation.alias }), { title: i18n.t('orgConfirm.revokeGrantTitle'), confirmLabel: i18n.t('orgConfirm.revokeGrantConfirm'), danger: true })
      if (!ok) return
    }
    setBusy(key)
    try {
      const res = existing
        ? await api.removeOrgGrant(orgName, role.id, automation.alias)
        : await api.setOrgGrant(orgName, defaultGrantSpec(role.id, automation))
      if (!res || res.error) notify(existing ? 'revoke grant' : 'grant automation', res?.error || 'failed')
      await onChanged?.()
    } finally {
      setBusy(null)
    }
  }

  if (!automations?.length) {
    return <div style={{ ...mono, fontSize: 11, color: 'var(--text-muted)', padding: 16 }}>No automations in this org yet. Add one from the Automations drawer.</div>
  }
  if (!roles.length) {
    return <div style={{ ...mono, fontSize: 11, color: 'var(--text-muted)', padding: 16 }}>No agent roles to grant to.</div>
  }

  return (
    <div style={{ overflow: 'auto', padding: 12, flex: 1, minHeight: 0 }}>
      <table role="grid" aria-label="Grants matrix" style={{ borderCollapse: 'separate', borderSpacing: 0, ...mono, fontSize: 11 }}>
        <thead>
          <tr>
            <th style={{ position: 'sticky', left: 0, background: 'var(--bg)', textAlign: 'left', padding: '6px 10px', color: 'var(--text-muted)', fontWeight: 500 }}>Role</th>
            {automations.map(a => (
              <th key={a.alias} scope="col" title={a.workflow_name} style={{ padding: '6px 10px', color: 'var(--text-secondary)', fontWeight: 500, whiteSpace: 'nowrap', borderBottom: '1px solid var(--border)' }}>
                <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
                  {a.alias}
                  {a.has_outbound_nodes && <Send size={9} color="var(--red, #ef4444)" aria-label="outbound" />}
                </span>
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {roles.map(role => (
            <tr key={role.id}>
              <th scope="row" style={{ position: 'sticky', left: 0, background: 'var(--bg)', textAlign: 'left', padding: '6px 10px', fontWeight: 500, color: 'var(--text)', whiteSpace: 'nowrap', borderRight: '1px solid var(--border)' }}>
                {role.title || role.id}
                {role.parentId == null && <span style={{ ...mutedText, marginLeft: 6 }}>boss</span>}
              </th>
              {automations.map(a => {
                const key = `${role.id}|${a.alias}`
                const g = byKey.get(key)
                const label = `${g ? 'Revoke' : 'Grant'} ${a.alias} ${g ? 'from' : 'to'} ${role.id}`
                return (
                  <td key={a.alias} style={{ padding: '4px 10px', textAlign: 'center', borderBottom: '1px solid var(--border-dim, var(--border))' }}>
                    <div style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
                      <button
                        aria-label={label}
                        aria-pressed={!!g}
                        disabled={busy === key}
                        onClick={() => toggle(role, a)}
                        style={{
                          ...cellBtn,
                          background: g ? 'rgba(0,180,216,0.12)' : 'transparent',
                          border: g ? '1px solid var(--cyan, #00b4d8)' : '1px dashed var(--border)',
                          color: g ? 'var(--cyan, #00b4d8)' : 'var(--text-muted)',
                        }}
                      >
                        {g ? <Check size={11} /> : <Plus size={11} />}
                      </button>
                      {g && g.approval === 'required' && <TierChip tier={g.tier} />}
                      {g && (
                        <button aria-label={`Edit grant ${a.alias} for ${role.id}`} onClick={() => onEditGrant?.(role, a, g)} style={{ background: 'none', border: 'none', color: 'var(--text-muted)', cursor: 'pointer', padding: 2 }}>
                          <Pencil size={10} />
                        </button>
                      )}
                    </div>
                  </td>
                )
              })}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
