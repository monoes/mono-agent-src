import { useTranslation } from 'react-i18next'
import { ShieldCheck, ShieldAlert, ShieldQuestion } from 'lucide-react'
import { clip, num } from './controlRoomModel.js'

// The integrity badge: "verified" only for chain_verified "yes"; anything else says it is not ("not anchored" is amber).
export function IntegrityBadge({ integrity }) {
  const { t } = useTranslation()
  const c = integrity?.chain_verified
  const kind = c === 'yes' || c === 'no' || c === 'unanchored' ? c : 'unknown'
  const Icon = kind === 'yes' ? ShieldCheck : kind === 'no' ? ShieldAlert : ShieldQuestion
  return (
    <span className={`cr-badge cr-badge-${kind}`} data-testid="cr-integrity" data-chain={kind}>
      <Icon size={13} aria-hidden="true" />
      {t(`controlRoom.integrity.${kind}`)}
      {kind === 'yes' && integrity?.head_prefix ? <code className="cr-mono"> #{num(integrity.head_seq)} {clip(integrity.head_prefix, 16)}</code> : null}
    </span>
  )
}

export default function RunHeader({ run, integrity }) {
  const { t } = useTranslation()
  if (!run) return (
    <div className="cr-header"><IntegrityBadge integrity={integrity} /></div>
  )
  const state = clip(run.state, 32)
  return (
    <div className="cr-header" data-testid="cr-run-header">
      <span className={`cr-badge cr-state-${run.stopping ? 'stopping' : run.active ? 'running' : 'ended'}`} data-testid="cr-run-state">
        {run.stopping && run.active ? t('controlRoom.run.stopping') : run.active ? t('controlRoom.run.active') : t('controlRoom.run.ended')}
        <span className="cr-mono"> ({state})</span>
      </span>
      <span className="cr-meta">{t('controlRoom.run.generation', { n: num(run.generation) })}</span>
      <span className="cr-meta cr-mono" title={t('controlRoom.run.charter')}>{clip(run.charter_id, 40)}</span>
      <IntegrityBadge integrity={integrity} />
    </div>
  )
}
