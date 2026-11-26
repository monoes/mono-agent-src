// Small shared presentational bits for the org unification UI, matching the
// inline-style language of OrgsPanel / orgdesigner (mono font, CSS vars).
import { TIER_COLORS } from './autonomyModel.js'
import { useTranslation } from 'react-i18next'
import i18nOr from '../../lib/i18nOr.js'

export const mono = { fontFamily: 'var(--font-mono)' }

export const smallBtn = {
  display: 'flex', alignItems: 'center', gap: 4,
  fontFamily: 'var(--font-mono)', fontSize: 10, padding: '4px 8px', borderRadius: 'var(--radius)',
  background: 'transparent', border: '1px solid var(--border)', color: 'var(--text-muted)', cursor: 'pointer',
  whiteSpace: 'nowrap',
}

export const sectionLabel = {
  fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)',
  textTransform: 'uppercase', letterSpacing: 1,
}

export const mutedText = { fontFamily: 'var(--font-mono)', fontSize: 10.5, color: 'var(--text-muted)' }

export function Chip({ children, color = 'var(--text-muted)', title, style }) {
  return (
    <span
      title={title}
      style={{
        display: 'inline-flex', alignItems: 'center', gap: 3,
        fontFamily: 'var(--font-mono)', fontSize: 9, color, lineHeight: '14px',
        border: `1px solid ${color}`, borderRadius: 8, padding: '0 6px', whiteSpace: 'nowrap',
        ...style,
      }}
    >
      {children}
    </span>
  )
}

export function TierChip({ tier }) {
  const { t } = useTranslation()
  if (!tier) return null
  const name = t(`ui6Orgs.tier_${tier}`, { defaultValue: tier })
  return <Chip color={TIER_COLORS[tier] || 'var(--text-muted)'} title={t('ui6Orgs.tier_title', { tier: name })}>{name}</Chip>
}

export function Badge({ count, style }) {
  const { t } = useTranslation()
  if (!count) return null
  return (
    <span
      aria-label={t('ui6Orgs.badge_waiting', { count })}
      style={{
        minWidth: 14, height: 14, padding: '0 3px', borderRadius: 7,
        background: 'var(--red, #ef4444)', color: '#fff',
        fontFamily: 'var(--font-mono)', fontSize: 9, fontWeight: 700, lineHeight: '14px', textAlign: 'center',
        ...style,
      }}
    >
      {count > 99 ? '99+' : count}
    </span>
  )
}

export function Card({ children, style }) {
  return (
    <div style={{ background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius-lg)', padding: '10px 14px', ...style }}>
      {children}
    </div>
  )
}

/** Payload helper shared by org components: CLI {error} or a null read. */
export function errorOf(res, fallback = i18nOr('ui6Orgs.request_failed')) {
  if (res == null) return fallback
  return res.error || null
}
