import { useState, useEffect } from 'react'
import { ChevronRight, ChevronDown, ShieldAlert } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { mono, label, hint, errText, input, formatUSD, Switch, EgressList, hostOf, isThisComputer, defaultTarget,
  surfacePinned, missingKinds, capabilityError, sampledCeiling } from './jevShared.jsx'
import i18nOr from '../../lib/i18nOr.js'

// Where a surface's data goes: its own provider, else the default. typesafe
// (or a CLI too old to say) keeps today's wording.
function surfaceTarget(s, status, providers) {
  const name = s.provider || status.provider || 'typesafe'
  const p = (providers?.providers || []).find(x => x.name === name)
  return { name, typesafe: name === 'typesafe', host: (p && hostOf(p.base_url)) || status.provider_host || name, base: p?.base_url || '' }
}

// The per-feature provider select ('' follows the default) and, when the
// feature is on and the new provider is on another host, the consent first.
// A provider whose probed kinds lack one the feature asks is offered
// disabled; the CLI's refusal (a kind it could not see) shows by the select.
// A feature with a provider of its own (pinned, even to the default's name)
// says so and offers to follow the default again.
function ProviderSelect({ s, title, status, providers, busy, onSet }) {
  const { t } = useTranslation()
  const [asking, setAsking] = useState(null)
  const [err, setErr] = useState('')
  const list = providers?.providers || []
  const def = providers?.default || status.provider || 'typesafe'
  const pinned = surfacePinned(s, def)
  const value = pinned ? s.provider : ''
  const current = surfaceTarget(s, status, providers)
  const selId = `jev-prov-${s.surface}`
  const errId = `jev-prov-err-${s.surface}`
  const set = (v) => { setErr(''); onSet(s.surface, v, e => setErr(capabilityError(e, t))) }
  const choose = (v) => {
    if (v === value) return
    const p = list.find(x => x.name === (v || def))
    const host = (p && hostOf(p.base_url)) || v || def
    if (s.enabled && !(p && isThisComputer(p.base_url)) && host !== current.host) setAsking({ v, host })
    else set(v)
  }
  return (
    <>
      <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
        <label htmlFor={selId} style={{ ...label, letterSpacing: 0.5 }}>{t('settings.jev.providers.featureProviderLabel')}</label>
        <select id={selId} className="filter-select" value={asking ? asking.v : value} disabled={busy}
          aria-label={t('settings.jev.providers.featureProvider', { feature: title })}
          aria-describedby={err ? errId : undefined} aria-invalid={!!err}
          onChange={e => choose(e.target.value)} style={{ fontSize: 10, padding: '2px 22px 2px 6px' }}>
          <option value="">{t('settings.jev.providers.defaultOption', { name: def })}</option>
          {list.map(p => {
            const missing = missingKinds(p, s.surface)
            return (
              <option key={p.name} value={p.name} disabled={missing.length > 0 && p.name !== value}>
                {missing.length ? t('settings.jev.providers.optionMissingKinds', { name: p.name, kinds: missing.join(', ') }) : p.name}
              </option>
            )
          })}
        </select>
        {pinned && (
          <>
            <span data-testid={`jev-pinned-${s.surface}`} style={{ fontFamily: mono, fontSize: 10, color: 'var(--text-secondary)' }}>
              {t('settings.jev.providers.pinnedTo', { name: s.provider })}
            </span>
            <button type="button" className="btn btn-secondary btn-sm" disabled={busy} onClick={() => choose('')}
              aria-label={t('settings.jev.providers.followDefaultAria', { feature: title, name: def })}
              style={{ fontSize: 10, padding: '1px 8px' }}>
              {t('settings.jev.providers.followDefault')}
            </button>
          </>
        )}
      </div>
      {/* Always rendered so a refusal is announced when it appears. */}
      <div aria-live="assertive" data-testid={`jev-provider-error-${s.surface}`} style={{ flexBasis: '100%' }}>
        {err && <div id={errId} role="alert" style={errText}>{err}</div>}
      </div>
      {asking && (
        <div data-testid={`jev-provider-confirm-${s.surface}`} style={{
          flexBasis: '100%', background: 'rgba(234,179,8,.05)', border: '1px solid rgba(234,179,8,.22)', borderRadius: 'var(--radius)',
          padding: '10px 12px', display: 'flex', flexDirection: 'column', gap: 8,
        }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 6, fontFamily: mono, fontSize: 11, color: 'var(--yellow)' }}>
            <ShieldAlert size={13} /> {t('settings.jev.providers.featureSwitchTitle', { feature: title, host: asking.host })}
          </div>
          <EgressList items={s.egress} />
          <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', flexWrap: 'wrap' }}>
            <button type="button" className="btn btn-secondary btn-sm" disabled={busy} onClick={() => setAsking(null)}>{t('settings.jev.providers.cancel')}</button>
            <button type="button" className="btn btn-primary btn-sm" disabled={busy}
              onClick={() => { const { v } = asking; setAsking(null); set(v) }}>
              {t('settings.jev.providers.featureSwitchConfirm', { host: asking.host })}
            </button>
          </div>
        </div>
      )}
    </>
  )
}

function ThresholdInput({ value, disabled, onChange, id }) {
  return (
    <input
      id={id} type="number" min={0.05} max={1} step={0.05} value={value} disabled={disabled}
      onChange={e => onChange(e.target.value)}
      style={{ ...input, width: 76, padding: '4px 8px' }}
    />
  )
}

// 0.05–1, or the surface's own default (people_review defaults to 0: suggest only).
const validThreshold = (v, def) => {
  const n = Number(v)
  return v !== '' && Number.isFinite(n) && ((n >= 0.05 && n <= 1) || n === def)
}

function SurfaceRow({ s, status, providers, withProviders, busy, pending, onStartEnable, onCancelEnable, onConfirmEnable, onDisable, onThreshold, onSetProvider }) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const target = surfaceTarget(s, status, providers)
  const [thr, setThr] = useState(String(s.threshold ?? s.default_threshold ?? ''))
  useEffect(() => { setThr(String(s.threshold ?? s.default_threshold ?? '')) }, [s.threshold, s.default_threshold, s.enabled])
  const title = s.title || s.surface
  const changed = s.enabled && validThreshold(thr, s.default_threshold) && Number(thr) !== s.threshold
  const thrId = `jev-thr-${s.surface}`
  // A sampled provider's top probability is at most sampledCeiling: a
  // threshold above it is never reached, unless the CLI says the feature
  // lowers it to that. api_auto's options vary, so its ceiling may be lower.
  const effective = (providers?.providers || []).find(p => p.name === target.name)
  const ceiling = sampledCeiling(s.surface)
  const sampled = (s.enabled || pending) && effective?.caps?.probability === 'sampled' &&
    validThreshold(thr, s.default_threshold)
  const lowers = s.sampled_lowers_threshold === true
  let sampledKey = ''
  if (sampled && Number(thr) > ceiling) sampledKey = lowers ? 'sampledLowered' : 'sampledUnreachable'
  else if (sampled && lowers && s.surface === 'api_auto' && Number(thr) > 0) sampledKey = 'sampledLoweredVariable'

  return (
    <div data-jev-surface={s.surface} style={{
      borderTop: '1px solid var(--border-dim)', paddingTop: 10, display: 'flex', flexDirection: 'column', gap: 6,
    }}>
      <div style={{ display: 'flex', alignItems: 'flex-start', gap: 12 }}>
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ display: 'flex', alignItems: 'baseline', gap: 8, flexWrap: 'wrap' }}>
            <span style={{ fontFamily: mono, fontSize: 12, fontWeight: 600, color: 'var(--text)' }}>{title}</span>
            <span style={{ fontFamily: mono, fontSize: 9.5, color: 'var(--text-dim)' }}>{s.surface}</span>
          </div>
          {s.description && <div style={{ ...hint, marginTop: 2 }}>{s.description}</div>}
          {withProviders && (
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap', marginTop: 6 }}>
              <ProviderSelect s={s} title={title} status={status} providers={providers} busy={busy} onSet={onSetProvider} />
            </div>
          )}
        </div>
        {s.enabled && (
          <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexShrink: 0 }}>
            <label htmlFor={thrId} style={{ ...label, letterSpacing: 0.5 }}>{t('ui6SetJev.threshold')}</label>
            <ThresholdInput id={thrId} value={thr} disabled={busy} onChange={setThr} />
            {changed && (
              <button className="btn btn-secondary btn-sm" disabled={busy} onClick={() => onThreshold(s.surface, Number(thr))}>{t('ui6SetJev.apply')}</button>
            )}
          </div>
        )}
        <Switch
          on={s.enabled || pending} disabled={busy} label={t(s.enabled ? 'ui6SetJev.switch_on' : 'ui6SetJev.switch_off', { title })}
          onChange={on => (on ? onStartEnable(s.surface) : (pending ? onCancelEnable() : onDisable(s.surface)))}
        />
      </div>

      {sampledKey && (
        <div data-testid={`jev-sampled-warning-${s.surface}`} role="note" style={{
          display: 'flex', alignItems: 'flex-start', gap: 6, fontFamily: 'var(--font-body)', fontSize: 11, lineHeight: 1.5,
          color: 'var(--yellow)', background: 'rgba(234,179,8,.05)', border: '1px solid rgba(234,179,8,.22)',
          borderRadius: 'var(--radius)', padding: '6px 10px',
        }}>
          <ShieldAlert size={13} style={{ flexShrink: 0, marginTop: 2 }} />
          <span>{t(`settings.jev.providers.${sampledKey}`, { max: ceiling.toFixed(2) })}</span>
        </div>
      )}

      {pending ? (
        <div data-testid={`jev-confirm-${s.surface}`} style={{
          background: 'rgba(234,179,8,.05)', border: '1px solid rgba(234,179,8,.22)', borderRadius: 'var(--radius)',
          padding: '10px 12px', display: 'flex', flexDirection: 'column', gap: 8,
        }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 6, fontFamily: mono, fontSize: 11, color: 'var(--yellow)' }}>
            <ShieldAlert size={13} /> {target.typesafe ? t('ui6SetJev.sends_typesafe') : t('settings.jev.providers.sendsTo', { host: target.host })}
          </div>
          <EgressList items={s.egress} />
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
            <label htmlFor={thrId} style={{ ...label, letterSpacing: 0.5 }}>{t('ui6SetJev.threshold')}</label>
            <ThresholdInput id={thrId} value={thr} disabled={busy} onChange={setThr} />
            <span style={{ ...hint, flex: 1, minWidth: 140 }}>{t('ui6SetJev.threshold_hint')}</span>
            <button className="btn btn-secondary btn-sm" disabled={busy} onClick={onCancelEnable}>{t('ui6Set.cancel')}</button>
            <button className="btn btn-primary btn-sm" disabled={busy || !validThreshold(thr, s.default_threshold)}
              onClick={() => onConfirmEnable(s.surface, Number(thr))}>
              {i18nOr('ui6SetJev.send_enable')}
            </button>
          </div>
        </div>
      ) : (
        <div>
          <button
            type="button" onClick={() => setOpen(o => !o)} aria-expanded={open}
            style={{ background: 'none', border: 'none', padding: 0, cursor: 'pointer', display: 'inline-flex', alignItems: 'center', gap: 4, fontFamily: mono, fontSize: 10, color: 'var(--cyan-dim)' }}
          >
            {open ? <ChevronDown size={12} /> : <ChevronRight size={12} />} {target.typesafe ? t('ui6SetJev.what_typesafe') : t('settings.jev.providers.whatIsSentTo', { host: target.host })}
          </button>
          {open && <div style={{ marginTop: 6 }}><EgressList items={s.egress} /></div>}
        </div>
      )}
    </div>
  )
}

function UsageTable({ usage, titles }) {
  const { t } = useTranslation()
  const rows = usage?.surfaces || []
  if (!rows.length) return <div style={hint}>{t('ui6SetJev.no_calls')}</div>
  // A CLI that reports providers may list a surface once per provider.
  const withProvider = rows.some(u => u.provider)
  const th = { ...label, fontSize: 9, textAlign: 'right', padding: '0 0 6px 12px', fontWeight: 400 }
  const td = { fontFamily: mono, fontSize: 11, color: 'var(--text-secondary)', textAlign: 'right', padding: '4px 0 4px 12px', whiteSpace: 'nowrap' }
  const row = (u, total) => (
    <tr key={total ? 'total' : `${u.surface}:${u.provider || ''}`} style={total ? { borderTop: '1px solid var(--border)' } : undefined}>
      <td style={{ ...td, textAlign: 'left', paddingLeft: 0, color: total ? 'var(--text)' : 'var(--text-secondary)', whiteSpace: 'normal' }}>
        {total ? t('ui6SetJev.total') : (titles[u.surface] || u.surface)}
      </td>
      {withProvider && <td style={{ ...td, textAlign: 'left' }}>{total ? '' : (u.provider || '')}</td>}
      <td style={td}>{u.calls}{u.failures ? <span style={{ color: 'var(--red)' }}> {t('ui6SetJev.failed', { count: u.failures })}</span> : null}</td>
      <td style={td}>{(u.input_tokens || 0).toLocaleString()}</td>
      <td style={td}>{formatUSD(u.estimated_usd)}</td>
    </tr>
  )
  return (
    <div style={{ overflowX: 'auto' }}>
      <table style={{ width: '100%', maxWidth: 640, borderCollapse: 'collapse' }}>
        <thead>
          <tr>
            <th style={{ ...th, textAlign: 'left', paddingLeft: 0 }}>{t('ui6SetJev.feature')}</th>
            {withProvider && <th style={{ ...th, textAlign: 'left' }}>{t('settings.jev.providers.usageProvider')}</th>}
            <th style={th}>{t('ui6SetJev.calls')}</th>
            <th style={th}>{t('ui6SetJev.input_tokens')}</th>
            <th style={th}>{t('ui6SetJev.est_cost')}</th>
          </tr>
        </thead>
        <tbody>
          {rows.map(u => row(u, false))}
          {rows.length > 1 && usage.total && row(usage.total, true)}
        </tbody>
      </table>
    </div>
  )
}

// The per-profile half: the consent note (profile part only), the feature
// toggles with their thresholds and the 7-day usage table.
// providers (optional) adds a per-feature provider select once a provider
// other than the built-in one is saved; without one the rows look as before.
export default function JevFeaturesBlock({ part, status, usage, usageErr, busy, pending, onPending, onClearErr, onSetSurface, providers, onSetSurfaceProvider }) {
  const { t } = useTranslation()
  const surfaces = status.surfaces || []
  const titles = Object.fromEntries(surfaces.map(s => [s.surface, s.title || s.surface]))
  const hasKey = status.key_source && status.key_source !== 'none'
  const disabled = !!busy
  const withProviders = !!onSetSurfaceProvider && (providers?.providers || []).some(p => !p.builtin)
  // The TypeSafe key only matters for features TypeSafe answers.
  const defaultHost = defaultTarget(status, providers)
  const needsTypeSafeKey =surfaces.some(s => s.enabled && surfaceTarget(s, status, providers).typesafe)

  return (
    <>
      {part === 'profile' && <div style={hint}>{defaultHost.typesafe ? t('settings.jev.consent') : t('settings.jev.providers.consentHost', { host: defaultHost.host })}</div>}
      <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
        <span style={label}>{i18nOr('ui6SetJev.features')}</span>
        {!hasKey && needsTypeSafeKey && (
          <div style={{ ...hint, color: 'var(--yellow)' }}>{i18nOr('ui6SetJev.inactive')}</div>
        )}
        {surfaces.length === 0 && <div style={hint}>{t('ui6SetJev.no_features')}</div>}
        {surfaces.map(s => (
          <SurfaceRow
            key={s.surface} s={s} busy={disabled} pending={pending === s.surface}
            status={status} providers={providers} withProviders={withProviders} onSetProvider={onSetSurfaceProvider}
            onStartEnable={name => { onClearErr(); onPending(name) }}
            onCancelEnable={() => onPending('')}
            onConfirmEnable={(name, thr) => onSetSurface(name, true, thr)}
            onDisable={name => onSetSurface(name, false, 0)}
            onThreshold={(name, thr) => onSetSurface(name, true, thr)}
          />
        ))}
      </div>

      {/* Usage */}
      <div style={{ display: 'flex', flexDirection: 'column', gap: 8, borderTop: '1px solid var(--border-dim)', paddingTop: 12 }}>
        <span style={label}>{t('ui6SetJev.usage')}</span>
        {usageErr ? <div style={errText}>{t('ui6SetJev.usage_failed', { error: usageErr })}</div> : <UsageTable usage={usage} titles={titles} />}
      </div>
    </>
  )
}
