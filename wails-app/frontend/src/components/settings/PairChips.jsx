import { useTranslation } from 'react-i18next'
import { X, ArrowUp, ArrowDown } from 'lucide-react'
import { pairKey, rosterStatus, STATUS_TONE } from '../../lib/agentPurposes.js'

// The chosen (system, model) pairs of one purpose as chips. Each chip carries
// the pair's current roster status; a pair that is no longer ok stays listed
// (so it can be removed) but is flagged. With the "in this order" rule the
// chips are numbered and can be moved.

const mono = 'var(--font-mono)'
const TONE = { ok: 'var(--green-neon)', warn: 'var(--yellow)', bad: 'var(--red)', muted: 'var(--text-muted)' }
const iconBtn = { padding: '4px 5px', lineHeight: 0 }

export function StatusChip({ status }) {
  const { t } = useTranslation()
  const tone = STATUS_TONE[status] || 'muted'
  return (
    <span data-testid="chip" data-status={status} data-tone={tone} style={{
      fontFamily: mono, fontSize: 9.5, color: TONE[tone], border: `1px solid ${TONE[tone]}`, borderRadius: 999,
      padding: '0 6px', whiteSpace: 'nowrap',
    }}>{t(`settings.purposes.status.${status}`)}</span>
  )
}

export default function PairChips({ pairs, roster, ordered, disabled, labelledBy, onChange }) {
  const { t } = useTranslation()
  if (pairs.length === 0) return null
  const move = (i, d) => {
    const next = pairs.slice()
    ;[next[i], next[i + d]] = [next[i + d], next[i]]
    onChange(next)
  }
  return (
    <ul aria-labelledby={labelledBy} style={{ listStyle: 'none', margin: 0, padding: 0, display: 'flex', flexWrap: 'wrap', gap: 6 }}>
      {pairs.map((p, i) => {
        const status = rosterStatus(roster, p)
        const label = `${p.runtime} / ${p.model}`
        return (
          <li key={pairKey(p)} data-pair={pairKey(p)} data-status={status} style={{
            position: 'relative', display: 'inline-flex', alignItems: 'center', gap: 6, maxWidth: '100%', padding: '3px 6px 3px 9px',
            border: `1px solid ${status === 'ok' ? 'var(--border-bright)' : TONE[STATUS_TONE[status]]}`,
            borderRadius: 'var(--radius)', background: 'var(--elevated)',
          }}>
            {ordered && <span style={{ fontFamily: mono, fontSize: 10, color: 'var(--text-muted)' }}>{i + 1}</span>}
            <span title={label} style={{ fontFamily: mono, fontSize: 11, color: 'var(--text)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', minWidth: 0 }}>{label}</span>
            <StatusChip status={status} />
            {status !== 'ok' && <span style={{ position: 'absolute', width: 1, height: 1, overflow: 'hidden', clip: 'rect(0 0 0 0)' }}>{t('settings.purposes.notUsable')}</span>}
            {ordered && pairs.length > 1 && (
              <>
                <button type="button" className="btn btn-ghost btn-sm" style={iconBtn} disabled={disabled || i === 0}
                  aria-label={t('settings.purposes.moveUp', { model: label })} onClick={() => move(i, -1)}><ArrowUp size={11} /></button>
                <button type="button" className="btn btn-ghost btn-sm" style={iconBtn} disabled={disabled || i === pairs.length - 1}
                  aria-label={t('settings.purposes.moveDown', { model: label })} onClick={() => move(i, 1)}><ArrowDown size={11} /></button>
              </>
            )}
            <button type="button" className="btn btn-ghost btn-sm" style={iconBtn} disabled={disabled}
              aria-label={t('settings.purposes.remove', { model: label })} onClick={() => onChange(pairs.filter((_, j) => j !== i))}><X size={11} /></button>
          </li>
        )
      })}
    </ul>
  )
}
