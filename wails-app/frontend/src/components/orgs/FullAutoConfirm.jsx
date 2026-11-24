// Confirmation shown before an org goes to Full auto (U16): lists what the
// decider will resolve with no human — gates, and grants whose row says
// tier "irreversible" — so the operator sees the blast radius first.
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { AlertTriangle } from 'lucide-react'
import { api } from '../../services/api.js'
import { fullAutoImpact } from './autonomyModel.js'
import { mono, mutedText } from './ui.jsx'
import { useEscapeClose } from '../../lib/escapeStack.js'

function itemsOf(payload, key) {
  if (!payload || payload.error) return []
  if (Array.isArray(payload[key])) return payload[key]
  if (Array.isArray(payload.items)) return payload.items
  return []
}

export default function FullAutoConfirm({ open, orgName, autonomy, deciderKind, onConfirm, onCancel }) {
  useEscapeClose(onCancel, !!open)
  const { t } = useTranslation()
  const [loading, setLoading] = useState(false)
  const [impact, setImpact] = useState(null)
  const [loadError, setLoadError] = useState('')

  useEffect(() => {
    if (!open || !orgName) return
    let cancelled = false
    setLoading(true)
    setLoadError('')
    Promise.all([api.listOrgGrants(orgName), api.getOrgGates(orgName)]).then(([grants, gates]) => {
      if (cancelled) return
      if (!grants || grants.error) setLoadError(grants?.error || t('orgs.fullAuto.grantsFailed'))
      const pending = itemsOf(gates, 'gates').filter(g => !g.status || g.status === 'pending')
      setImpact(fullAutoImpact(autonomy, itemsOf(grants, 'grants'), pending))
      setLoading(false)
    })
    return () => { cancelled = true }
  }, [open, orgName, autonomy, t])

  if (!open) return null
  const decider = deciderKind || autonomy?.decider?.kind || 'model'

  return (
    <div className="modal-overlay" onClick={e => { if (e.target === e.currentTarget) onCancel() }}>
      <div role="dialog" aria-modal="true" aria-label={t('orgs.fullAuto.dialog')} className="modal" style={{ width: 480 }}>
        <div className="modal-title" style={{ justifyContent: 'flex-start', gap: 8 }}>
          <AlertTriangle size={16} color="var(--red, #ef4444)" /> {t('orgs.fullAuto.title', { org: orgName })}
        </div>
        <div style={{ ...mono, fontSize: 11.5, color: 'var(--text-secondary)', marginBottom: 12 }}>
          {t('orgs.fullAuto.intro', { decider: t(`orgs.autonomy.deciders.${decider}`, { defaultValue: decider }) })}
        </div>
        {loading ? (
          <div style={{ display: 'flex', justifyContent: 'center', padding: 12 }}><div className="spinner" /></div>
        ) : (
          <ul style={{ ...mono, fontSize: 11.5, color: 'var(--text)', margin: 0, paddingLeft: 18, display: 'flex', flexDirection: 'column', gap: 6 }}>
            {impact?.gatesIncluded && (
              <li>
                {t('orgs.fullAuto.everyGate')}
                {impact.pendingGates.length > 0 && (
                  <span style={mutedText}>{t('orgs.fullAuto.pendingNow', { gates: impact.pendingGates.map(g => `${g.name}${g.role ? ` (${g.role})` : ''}`).join(', ') })}</span>
                )}
              </li>
            )}
            {impact?.grants.map(g => (
              <li key={`${g.role}:${g.alias}`}>
                <b>{g.role}</b> {t('orgs.fullAuto.running')} <b>{g.workflowName}</b> <span style={mutedText}>{t('orgs.fullAuto.outbound')}</span>
              </li>
            ))}
            {impact?.classes.map(c => <li key={c}>{c} <span style={mutedText}>{t('orgs.fullAuto.setIrreversible')}</span></li>)}
            {impact && !impact.gatesIncluded && impact.grants.length === 0 && impact.classes.length === 0 && (
              <li style={mutedText}>{t('orgs.fullAuto.none')}</li>
            )}
          </ul>
        )}
        {loadError && <div style={{ ...mono, fontSize: 10.5, color: '#f87171', marginTop: 10 }}>{loadError} {t('orgs.fullAuto.incomplete')}</div>}
        <div className="modal-actions">
          <button className="btn btn-ghost" onClick={onCancel}>{t('orgs.fullAuto.cancel')}</button>
          <button className="btn btn-danger" onClick={onConfirm} disabled={loading}>{t('orgs.fullAuto.confirm')}</button>
        </div>
      </div>
    </div>
  )
}
