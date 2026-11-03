import { useTranslation } from 'react-i18next'
import { spendState, formatMicro, clip, num } from './controlRoomModel.js'

// cap / used, the approximate rate the page computed from its own polls (the CLI reports none), and the cumulative
// counters with the charter's limits. No ceiling is its own state, not an empty bar.
export default function SpendMeter({ spend, rate }) {
  const { t } = useTranslation()
  const s = spendState(spend)
  const counters = Array.isArray(spend?.counters) ? spend.counters.slice(0, 20) : []
  return (
    <section className="cr-card" aria-label={t('controlRoom.spend.title')} data-testid="cr-spend" data-spend={s.kind}>
      <h3>{t('controlRoom.spend.title')}</h3>
      <p className="cr-big">{formatMicro(s.used)}{s.cap ? <span className="cr-meta"> / {formatMicro(s.cap)}</span> : null}</p>
      {s.cap ? (
        <div className={`cr-meter cr-meter-${s.kind}`} role="meter" aria-valuemin={0} aria-valuemax={100}
          aria-valuenow={Math.round(s.frac * 100)} aria-label={t('controlRoom.spend.meter')}>
          <span style={{ width: `${s.frac * 100}%` }} />
        </div>
      ) : null}
      {s.kind === 'no_ceiling' ? <p className="cr-warn">{t('controlRoom.spend.noCeiling')}</p> : null}
      {s.kind === 'no_budget' ? <p className="cr-meta">{t('controlRoom.spend.noBudget')}</p> : null}
      {s.kind === 'over' ? <p className="cr-warn">{t('controlRoom.spend.over')}</p> : null}
      <p className="cr-meta" data-testid="cr-rate">
        {rate == null ? t('controlRoom.spend.rateNone') : t('controlRoom.spend.rate', { rate: formatMicro(rate) })}
      </p>
      <p className="cr-meta">{t('controlRoom.spend.split', { ask: formatMicro(spend?.ask_micro), grant: formatMicro(spend?.grant_micro) })}</p>
      {counters.length > 0 ? (
        <ul className="cr-counters">
          {counters.map(c => (
            <li key={clip(c?.kind, 40)}><span className="cr-mono">{clip(c?.kind, 40)}</span> {num(c?.total)}{typeof c?.limit === 'number' ? ` / ${c.limit}` : ''}</li>
          ))}
        </ul>
      ) : null}
    </section>
  )
}
