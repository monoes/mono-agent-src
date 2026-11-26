// Grant dialog (plan §7.1, §7.4): lets an agent role call an automation as a
// tool. The approval control reads "Needs a decision: yes / no" and previews
// the tier the call will get and who decides it at the org's current level.
// Saves through `org grant add` only — never a JSON edit (C-3).
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { KeyRound, AlertTriangle, FileWarning } from 'lucide-react'
import { api } from '../../services/api.js'
import {
  tierForClass, effectiveLevel, routeFor, routeLabel, needsDecisionToApproval, approvalToNeedsDecision,
} from '../orgs/autonomyModel.js'
import { TierChip, mono, mutedText } from '../orgs/ui.jsx'
import { useEscapeClose } from '../../lib/escapeStack.js'

export const GRANT_DEFAULTS = { mode: 'run', wait: true, timeout_seconds: 600, max_calls_per_run: 20 }

const MODES = [
  { id: 'run', labelKey: 'ui6OrgsDesigner.mode_run', hintKey: 'ui6OrgsDesigner.mode_run_hint' },
  { id: 'trigger', labelKey: 'ui6OrgsDesigner.mode_trigger', hintKey: 'ui6OrgsDesigner.mode_trigger_hint' },
  { id: 'status', labelKey: 'ui6OrgsDesigner.mode_status', hintKey: 'ui6OrgsDesigner.mode_status_hint' },
]

/** Whether a role's tool policy still allows Bash (plan §8 layer 8). */
export function bashAllowed(node) {
  const deny = node?.rest?.policy?.denyTools || []
  return !deny.includes('Bash')
}

/** Default grant spec for a role × automation (outbound work needs a decision). */
export function defaultGrantSpec(roleId, automation) {
  return {
    role: roleId,
    automation: automation.alias,
    ...GRANT_DEFAULTS,
    approval: automation.has_outbound_nodes ? 'required' : 'none',
  }
}

/**
 * File nodes of the automation whose paths come from the run's input
 * (`org automation list` → file_input_nodes, C-46), and those among them
 * that cannot be confined to the calling role's workdir.
 */
export function fileInputSummary(automation) {
  const nodes = Array.isArray(automation?.file_input_nodes) ? automation.file_input_nodes : []
  return { nodes, unconfined: nodes.filter(n => !n.confined) }
}

function num(v, fallback) {
  const n = Number(v)
  return Number.isFinite(n) && n > 0 ? Math.floor(n) : fallback
}

export default function GrantDialog({ open, orgName, role, automation, grant, autonomy, onClose, onSaved, onDenyBash }) {
  useEscapeClose(onClose, !!open)
  const { t } = useTranslation()
  const [mode, setMode] = useState(GRANT_DEFAULTS.mode)
  const [wait, setWait] = useState(GRANT_DEFAULTS.wait)
  const [timeout, setTimeoutSec] = useState(String(GRANT_DEFAULTS.timeout_seconds))
  const [needsDecision, setNeedsDecision] = useState(false)
  const [cap, setCap] = useState(String(GRANT_DEFAULTS.max_calls_per_run))
  const [denyBash, setDenyBash] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    if (!open || !automation) return
    setMode(grant?.mode || GRANT_DEFAULTS.mode)
    setWait(grant?.wait ?? GRANT_DEFAULTS.wait)
    setTimeoutSec(String(grant?.timeout_seconds ?? GRANT_DEFAULTS.timeout_seconds))
    setNeedsDecision(grant ? approvalToNeedsDecision(grant.approval) : !!automation.has_outbound_nodes)
    setCap(String(grant?.max_calls_per_run ?? GRANT_DEFAULTS.max_calls_per_run))
    setDenyBash(true)
    setError('')
    setSaving(false)
  }, [open, automation, grant])

  if (!open || !role || !automation) return null

  const cls = `grant:${automation.alias}`
  const tier = grant?.tier && approvalToNeedsDecision(grant.approval) === needsDecision
    ? grant.tier
    : tierForClass(cls, autonomy, { hasOutbound: !!automation.has_outbound_nodes })
  const level = effectiveLevel(autonomy)
  const route = routeFor(level, tier)
  const showBash = bashAllowed(role)
  const fileInputs = fileInputSummary(automation)

  const save = async () => {
    setSaving(true)
    setError('')
    const spec = {
      role: role.id,
      automation: automation.alias,
      mode,
      wait,
      timeout_seconds: num(timeout, GRANT_DEFAULTS.timeout_seconds),
      approval: needsDecisionToApproval(needsDecision),
      max_calls_per_run: num(cap, GRANT_DEFAULTS.max_calls_per_run),
    }
    // `org grant add` is an upsert that rebuilds the whole row from its
    // flags, so caps this dialog doesn't model have to be resent verbatim —
    // otherwise editing the timeout here silently resets a daily or output
    // cap the operator set on the CLI back to the CLI's own default.
    if (grant?.max_calls_per_day > 0) spec.max_calls_per_day = grant.max_calls_per_day
    if (grant?.max_output_bytes > 0) spec.max_output_bytes = grant.max_output_bytes
    const res = await api.setOrgGrant(orgName, spec)
    setSaving(false)
    if (!res || res.error) {
      setError(res?.error || t('ui6OrgsDesigner.grant_save_failed'))
      return
    }
    if (showBash && denyBash) onDenyBash?.(role)
    onSaved?.(res)
  }

  const label = { ...mono, fontSize: 10.5, color: 'var(--text-secondary)', display: 'flex', alignItems: 'center', gap: 6 }

  return (
    <div className="modal-overlay" onClick={e => { if (e.target === e.currentTarget) onClose() }}>
      <div role="dialog" aria-modal="true" aria-label={t('ui6OrgsDesigner.grant_automation')} className="modal" style={{ width: 460 }}>
        <div className="modal-title" style={{ justifyContent: 'flex-start', gap: 8 }}>
          <KeyRound size={15} /> {grant ? t('ui6OrgsDesigner.grant_edit') : t('ui6OrgsDesigner.grant_automation')}
        </div>
        <div style={{ ...mono, fontSize: 11.5, color: 'var(--text-secondary)', marginBottom: 14 }}>
          <b style={{ color: 'var(--text)' }}>{role.title || role.id}</b> {t('ui6OrgsDesigner.grant_may_run')} <b style={{ color: 'var(--text)' }}>{automation.workflow_name || automation.alias}</b> {t('ui6OrgsDesigner.grant_as_tool')} <code>monoagent__automation_{automation.alias}</code>.
        </div>

        <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          <label style={label}>
            {t('ui6OrgsDesigner.mode')}
            <select aria-label={t('ui6OrgsDesigner.mode')} className="form-select" value={mode} onChange={e => setMode(e.target.value)} style={{ fontSize: 11, padding: '4px 28px 4px 8px', width: 'auto' }}>
              {MODES.map(m => <option key={m.id} value={m.id} title={t(m.hintKey)}>{t(m.labelKey)}</option>)}
            </select>
          </label>

          <label style={label}>
            <input type="checkbox" checked={wait} onChange={e => setWait(e.target.checked)} />
            {t('ui6OrgsDesigner.wait_result')}
            <span style={mutedText}>{t('ui6OrgsDesigner.wait_off')}</span>
          </label>

          <label style={label}>
            {t('ui6OrgsDesigner.timeout')}
            <input aria-label={t('ui6OrgsDesigner.timeout_seconds')} type="number" min="1" className="form-input" value={timeout} onChange={e => setTimeoutSec(e.target.value)} style={{ width: 90, fontSize: 11, padding: '4px 8px' }} />
            <span style={mutedText}>{t('ui6OrgsDesigner.seconds')}</span>
          </label>

          <div role="radiogroup" aria-label={t('ui6OrgsDesigner.needs_decision')} style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
            <div style={label}>
              {t('ui6OrgsDesigner.needs_decision_colon')}
              <label style={{ display: 'flex', alignItems: 'center', gap: 3 }}>
                <input type="radio" name="needs-decision" checked={needsDecision} onChange={() => setNeedsDecision(true)} aria-label={t('ui6OrgsDesigner.yes')} /> {t('ui6OrgsDesigner.yes')}
              </label>
              <label style={{ display: 'flex', alignItems: 'center', gap: 3 }}>
                <input type="radio" name="needs-decision" checked={!needsDecision} onChange={() => setNeedsDecision(false)} aria-label={t('ui6OrgsDesigner.no')} /> {t('ui6OrgsDesigner.no')}
              </label>
            </div>
            <div data-testid="grant-tier" style={{ display: 'flex', alignItems: 'center', gap: 6, ...mutedText }}>
              {needsDecision ? (
                <>
                  <TierChip tier={tier} />
                  <span>{t('ui6OrgsDesigner.at_level_route', { level: t(`ui6Orgs.level_${level}`, { defaultValue: level }), route: routeLabel(route, autonomy?.decider?.kind) })}</span>
                </>
              ) : (
                <span>{t('ui6OrgsDesigner.runs_without_decision')}</span>
              )}
            </div>
            {!needsDecision && automation.has_outbound_nodes && (
              <div style={{ ...mono, fontSize: 10.5, color: '#eab308' }}>{t('ui6OrgsDesigner.sends_outside')}</div>
            )}
          </div>

          {fileInputs.nodes.length > 0 && (
            <div data-testid="grant-file-inputs" style={{ display: 'flex', flexDirection: 'column', gap: 4, padding: '8px 10px', border: '1px solid #eab308', borderRadius: 'var(--radius)', background: 'rgba(234,179,8,0.08)' }}>
              <div style={{ ...mono, fontSize: 11, color: '#eab308', display: 'flex', alignItems: 'center', gap: 6 }}>
                <FileWarning size={12} /> {t('grantDialog.fileInput.title')}
              </div>
              <ul style={{ ...mono, fontSize: 10.5, color: 'var(--text-secondary)', margin: 0, paddingLeft: 18 }}>
                {fileInputs.nodes.map(n => (
                  <li key={`${n.node}:${n.field}`}>{n.node}: {t(`grantDialog.fileInput.access.${n.access}`)} <code>{n.field}</code></li>
                ))}
              </ul>
              <div style={{ ...mutedText, fontSize: 10.5 }}>{t('grantDialog.fileInput.confined', { role: role.title || role.id })}</div>
              {fileInputs.unconfined.length > 0 && (
                <div style={{ ...mono, fontSize: 10.5, color: 'var(--red, #ef4444)' }}>
                  {t('grantDialog.fileInput.unconfined', { nodes: fileInputs.unconfined.map(n => n.node).join(', ') })}
                </div>
              )}
            </div>
          )}

          <label style={label}>
            {t('ui6OrgsDesigner.per_run_cap')}
            <input aria-label={t('ui6OrgsDesigner.per_run_cap')} type="number" min="1" className="form-input" value={cap} onChange={e => setCap(e.target.value)} style={{ width: 90, fontSize: 11, padding: '4px 8px' }} />
            <span style={mutedText}>{t('ui6OrgsDesigner.calls')}</span>
          </label>

          {showBash && (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 4, padding: '8px 10px', border: '1px solid var(--red, #ef4444)', borderRadius: 'var(--radius)', background: 'rgba(239,68,68,0.08)' }}>
              <div style={{ ...mono, fontSize: 11, color: 'var(--red, #ef4444)', display: 'flex', alignItems: 'center', gap: 6 }}>
                <AlertTriangle size={12} /> {t('ui6OrgsDesigner.bash_bypass')}
              </div>
              <label style={label}>
                <input type="checkbox" checked={denyBash} onChange={e => setDenyBash(e.target.checked)} />
                {t('ui6OrgsDesigner.deny_bash_for', { role: role.title || role.id })}
              </label>
            </div>
          )}
        </div>

        {error && <div style={{ ...mono, fontSize: 11, color: '#f87171', marginTop: 12 }}>{error}</div>}
        <div className="modal-actions">
          <button className="btn btn-ghost" onClick={onClose}>{t('ui6OrgsDesigner.cancel')}</button>
          <button className="btn btn-primary" onClick={save} disabled={saving}>{saving ? t('ui6OrgsDesigner.saving') : grant ? t('ui6OrgsDesigner.save_grant') : t('ui6OrgsDesigner.grant')}</button>
        </div>
      </div>
    </div>
  )
}
