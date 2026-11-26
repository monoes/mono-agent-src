import { runtimeLabel } from '../../lib/runtimeLabels.js'
import { pickerRuntimes, runtimeChoice, useSectionsRuntimePolicy } from './sectionsRuntimes.js'
import { useTranslation } from 'react-i18next'

// The role inspector's runtime picker. In a sections org it disables the
// runtimes monomind refuses (showing its reason) and marks the unverified
// ones with a warning, which still can be chosen (#341).
export default function RuntimeSelect({ value, options, sectionsOrg, onChange }) {
  const { t } = useTranslation()
  const policy = useSectionsRuntimePolicy(!!sectionsOrg)
  const current = runtimeChoice(policy, value)
  return (
    <>
      <select className="form-select" value={value} onChange={e => onChange(e.target.value)}>
        <option value="">{t('ui6OrgsMore.inherit_runtime')}</option>
        {pickerRuntimes(options, policy).map(r => {
          const c = runtimeChoice(policy, r)
          const label = c.status === 'unverified' ? t('ui6OrgsMore.runtime_unverified', { runtime: runtimeLabel(r) })
            : c.status === 'refused' ? t('ui6OrgsMore.runtime_refused', { runtime: runtimeLabel(r) })
            : runtimeLabel(r)
          return <option key={r} value={r} disabled={c.status === 'refused' && r !== value} title={c.reason}>{label}</option>
        })}
      </select>
      {current.status !== 'ok' && (
        <div
          role={current.status === 'refused' ? 'alert' : 'status'}
          data-testid="runtime-policy-note"
          style={{ fontSize: 11, marginTop: 4, color: current.status === 'refused' ? 'var(--danger, #c0392b)' : 'var(--warning, #b7791f)' }}
        >
          {current.status === 'unverified' ? t('ui6OrgsMore.unverified_prefix', { reason: current.reason }) : current.reason}
        </div>
      )}
    </>
  )
}
