import { useState, useEffect, useCallback, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { api } from '../../services/api.js'
import { confirm } from '../ConfirmDialog.jsx'

// AutoRevalidate is the "re-check stale models automatically" setting of
// the roster (monoes/mono-agent#230). The daemon does the work; this only
// shows `agent roster auto-revalidate status` and turns it on or off. Off
// by default: every re-check is a paid model call, so turning it on asks
// first and shows the estimated cost.

const mono = { fontFamily: 'var(--font-mono)' }

// formatUSD is the "≈ $x" of an estimate or spend; unknown counts calls
// that reported no cost.
export function formatUSD(t, usd, unknown) {
  const v = Number(usd) || 0
  let s = v > 0 && v < 0.0001 ? '≈ <$0.0001' : `≈ $${v.toFixed(4)}`
  if (unknown > 0) s += ' ' + t('agents.roster.auto.unknownCost', { count: unknown })
  return s
}

// nextCost is the next run's estimate, saying how many of its calls were
// priced from the CLI's built-in price table.
function nextCost(t, next) {
  const s = formatUSD(t, next.est_cost_usd, next.unknown_cost)
  return next.table_estimated > 0 ? s + ' ' + t('agents.roster.auto.fromTable', { count: next.table_estimated }) : s
}

// ceilingText is the most a day can cost, priced at the priciest model
// with a known cost.
function ceilingText(t, st) {
  const args = { perDay: st?.max_runtimes_per_day ?? 1, models: st?.max_models_per_run ?? 3 }
  return st?.cost_known
    ? t('agents.roster.auto.ceiling', { ...args, cost: formatUSD(t, st.daily_max_usd), priciest: formatUSD(t, st.priciest_model_usd) })
    : t('agents.roster.auto.ceilingUnknown', args)
}

const isSet = ts => !!ts && !String(ts).startsWith('0001-')

export default function AutoRevalidate({ refreshKey = 0 }) {
  const { t } = useTranslation()
  const [st, setSt] = useState(null)
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)

  // Only the newest answer counts: a reload that started before a toggle (the roster
  // changed while the user was confirming) can arrive after it with the old state.
  const latest = useRef(0)
  const load = useCallback(async () => {
    const mine = ++latest.current
    const res = await api.agentRosterAutoRevalidate()
    if (mine !== latest.current) return
    if (res?.error) { setError(res.error); return }
    setError('')
    setSt(res)
  }, [])
  useEffect(() => { load() }, [load, refreshKey])

  const toggle = useCallback(async on => {
    if (on) {
      const next = st?.next
      const cost = next && st?.next_unchecked
        ? t('agents.roster.auto.confirmNextUnchecked', { count: next.targets?.length || 0, cost: nextCost(t, next) })
        : next
        ? t('agents.roster.auto.confirmNext', { count: next.targets?.length || 0, runtime: next.runtime, cost: nextCost(t, next) })
        : t('agents.roster.auto.confirmNothing')
      const ok = await confirm(
        <span>{t('agents.roster.auto.confirmBody', { perDay: st?.max_runtimes_per_day ?? 1, models: st?.max_models_per_run ?? 3 })} {cost} {ceilingText(t, st)}</span>,
        { title: t('agents.roster.auto.confirmTitle'), confirmLabel: t('agents.roster.auto.turnOn'), cancelLabel: t('agents.cancel'), danger: false },
      )
      if (!ok) return
    }
    setSaving(true)
    const res = await api.setAgentRosterAutoRevalidate(on)
    setSaving(false)
    latest.current++ // the user's own answer is newest: reloads still in flight are older
    if (res?.error) { setError(res.error); return }
    setError('')
    setSt(res)
  }, [st, t])

  if (!st && !error) return null
  const enabled = !!st?.enabled
  const state = st?.state || {}
  const next = st?.next
  return (
    <div data-testid="auto-revalidate" style={{
      display: 'flex', flexDirection: 'column', gap: 3, padding: '8px 10px',
      border: '1px solid var(--border)', borderRadius: 'var(--radius-lg)', background: 'var(--surface)',
    }}>
      <label style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 12, color: 'var(--text)', cursor: saving ? 'wait' : 'pointer' }}>
        <input type="checkbox" checked={enabled} disabled={saving || !st} onChange={e => toggle(e.target.checked)} />
        {t('agents.roster.auto.label')}
        <span style={{ ...mono, fontSize: 10, color: enabled ? 'var(--green-neon)' : 'var(--text-muted)' }}>
          {enabled ? t('agents.roster.auto.on') : t('agents.roster.auto.off')}
        </span>
      </label>
      <div style={{ fontSize: 11, color: 'var(--text-muted)' }}>
        {t('agents.roster.auto.help', { perDay: st?.max_runtimes_per_day ?? 1, models: st?.max_models_per_run ?? 3, quiet: st?.quiet_period || '15m' })}
        {' '}<strong style={{ color: 'var(--yellow)', fontWeight: 500 }}>{t('agents.roster.auto.money')}</strong>
      </div>
      {st && (
        <div role="status" style={{ ...mono, fontSize: 10, color: 'var(--text-muted)', display: 'flex', flexWrap: 'wrap', gap: '2px 12px' }}>
          <span>{next && st.next_unchecked
            ? t('agents.roster.auto.nextUnchecked', { count: next.targets?.length || 0, cost: nextCost(t, next) })
            : next
            ? t('agents.roster.auto.next', { count: next.targets?.length || 0, runtime: next.runtime, cost: nextCost(t, next) })
            : t('agents.roster.auto.nothingStale')}</span>
          <span>{ceilingText(t, st)}</span>
          {st.state_error
            ? <span style={{ color: 'var(--yellow)' }}>{t('agents.roster.auto.stateUnreadable')}</span>
            : <span>{t('agents.roster.auto.today', { count: state.runtimes_today || 0, spent: formatUSD(t, state.spent_today_usd, state.unknown_cost_calls_today) })}</span>}
          {isSet(state.last_run_at) && (
            <span>{t('agents.roster.auto.lastRun', { at: new Date(state.last_run_at).toLocaleString(), runtime: state.last_runtime || '' })}</span>
          )}
          {enabled && isSet(state.next_eligible_at) && (
            <span>{t('agents.roster.auto.nextEligible', { at: new Date(state.next_eligible_at).toLocaleString() })}</span>
          )}
          {enabled && !st.daemon_running && (
            <span style={{ color: 'var(--yellow)' }}>{t('agents.roster.auto.noDaemon')}</span>
          )}
        </div>
      )}
      {error && <div role="alert" style={{ ...mono, fontSize: 10, color: 'var(--red)' }}>{error}</div>}
    </div>
  )
}
