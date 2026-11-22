import { useState } from 'react'
import { ShieldAlert, Plus } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { JevUseProvider, JevProbeProvider, JevRemoveProvider } from '../../wailsjs/go/main/App'
import { mono, label, hint, errText, okText, pill, hostOf, isThisComputer, testError, removeError, EgressList, surfacePinned } from './jevShared.jsx'
import JevAddProviderForm from './JevAddProviderForm.jsx'

// Settings → Jev → "Decision model": the saved decision providers (machine-wide),
// which one is the default, a probe per provider, removal and the add form.
// Everything goes through `monoagentcli jev provider …` (app_jev_providers.go).
// Making a provider on another host the default (TypeSafe too) first lists
// what follows the default and would send there: JevUseProvider passes --yes,
// so this is the consent `jev provider use` asks for in a terminal.

const PROB_STYLE = {
  native: ['var(--green-neon)', 'rgba(16,185,129,.1)', 'rgba(74,222,128,.25)'],
  logprob: ['var(--cyan)', 'rgba(0,180,216,.1)', 'rgba(0,180,216,.25)'],
  softmax: ['var(--yellow)', 'rgba(234,179,8,.08)', 'rgba(234,179,8,.22)'],
  sampled: ['var(--yellow)', 'rgba(234,179,8,.08)', 'rgba(234,179,8,.22)'],
  none: ['var(--text-muted)', 'rgba(255,255,255,.05)', 'var(--border)'],
}

// capsText says what a provider answers and its limits; a limit of 0 is no
// limit and is not shown. '' when nothing is known (not probed).
function capsText(caps, t) {
  const parts = []
  if (caps?.kinds?.length) parts.push(t('settings.jev.providers.capsKinds', { kinds: caps.kinds.join(', ') }))
  if (caps?.max_choice_options > 0) parts.push(t('settings.jev.providers.capsMaxOptions', { count: caps.max_choice_options }))
  if (caps?.options_per_request > 0) parts.push(t('settings.jev.providers.capsOptionsPerRequest', { count: caps.options_per_request }))
  return parts.join(' · ')
}

function ProviderRow({ p, isDefault, inUse, busy, probe, onUse, onTest, onRemove, confirmRemove, removeErr, onCancelRemove, onConfirmRemove }) {
  const { t } = useTranslation()
  const prob = p.caps?.probability || 'none'
  const why = p.builtin ? t('settings.jev.providers.whyBuiltin')
    : isDefault ? t('settings.jev.providers.whyDefault')
      : inUse ? t('settings.jev.providers.whyInUse') : ''
  const whyId = `jev-prov-why-${p.name}`
  const host = hostOf(p.base_url) || p.base_url
  const caps = capsText(p.caps, t)
  return (
    <div data-jev-provider={p.name} style={{ borderTop: '1px solid var(--border-dim)', paddingTop: 10, display: 'flex', flexDirection: 'column', gap: 6 }}>
      <div style={{ display: 'flex', alignItems: 'flex-start', gap: 12, flexWrap: 'wrap' }}>
        <div style={{ flex: 1, minWidth: 200, display: 'flex', flexDirection: 'column', gap: 4 }}>
          <div style={{ display: 'flex', alignItems: 'baseline', gap: 8, flexWrap: 'wrap' }}>
            <span style={{ fontFamily: mono, fontSize: 12, fontWeight: 600, color: 'var(--text)' }}>{p.name}</span>
            <span style={{ fontFamily: mono, fontSize: 9.5, color: 'var(--text-dim)' }}>{p.kind}{p.builtin ? ` · ${t('settings.jev.providers.builtin')}` : ''}</span>
            {isDefault && <span data-testid="jev-provider-default" style={pill('var(--cyan)', 'rgba(0,180,216,.1)', 'rgba(0,180,216,.25)')}>{t('settings.jev.providers.default')}</span>}
          </div>
          <div style={{ fontFamily: mono, fontSize: 10, color: 'var(--text-dim)', wordBreak: 'break-all' }}>
            {host}{p.model ? ` · ${p.model}` : ''}
          </div>
          <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
            <span title={t('settings.jev.providers.probTitle')} style={pill(...(PROB_STYLE[prob] || PROB_STYLE.none))}>
              {t(`settings.jev.providers.prob.${prob}`, { defaultValue: prob })}
            </span>
            <span style={p.key_set ? pill('var(--green-neon)', 'rgba(16,185,129,.1)', 'rgba(74,222,128,.25)') : pill('var(--text-muted)', 'rgba(255,255,255,.05)', 'var(--border)')}>
              {p.key_set ? t('settings.jev.providers.keySet') : t('settings.jev.providers.noKey')}
            </span>
          </div>
          {caps && <div style={{ fontFamily: mono, fontSize: 10, color: 'var(--text-dim)' }}>{caps}</div>}
          {(prob === 'sampled' || prob === 'softmax') && (
            <div data-testid={`jev-prob-note-${p.name}`} style={{ ...hint, color: 'var(--yellow)' }}>{t(`settings.jev.providers.probNote.${prob}`)}</div>
          )}
        </div>
        <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
          {!isDefault && (
            <button type="button" className="btn btn-secondary btn-sm" disabled={busy} onClick={onUse}
              aria-label={t('settings.jev.providers.useAria', { name: p.name })}>
              {t('settings.jev.providers.useDefault')}
            </button>
          )}
          <button type="button" className="btn btn-secondary btn-sm" disabled={busy} onClick={onTest}
            aria-label={t('settings.jev.providers.testAria', { name: p.name })}>
            {probe?.running ? t('settings.jev.providers.testing') : t('settings.jev.providers.test')}
          </button>
          {!confirmRemove && (
            <button type="button" className="btn btn-danger btn-sm" disabled={busy || !!why} onClick={onRemove}
              aria-label={t('settings.jev.providers.removeAria', { name: p.name })} aria-describedby={why ? whyId : undefined}>
              {t('settings.jev.providers.remove')}
            </button>
          )}
        </div>
      </div>
      {why && <div id={whyId} style={{ ...hint, textAlign: 'right' }}>{why}</div>}
      {confirmRemove && (
        <div data-testid={`jev-provider-remove-confirm-${p.name}`} style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
          <span style={{ fontFamily: mono, fontSize: 11, color: 'var(--red)', flex: 1, minWidth: 180 }}>
            {t('settings.jev.providers.confirmRemove', { name: p.name })}
          </span>
          <button type="button" className="btn btn-secondary btn-sm" disabled={busy} onClick={onCancelRemove}>{t('settings.jev.providers.cancel')}</button>
          <button type="button" className="btn btn-danger btn-sm" disabled={busy} onClick={onConfirmRemove}>
            {busy ? t('settings.jev.providers.deleting') : t('settings.jev.providers.confirmRemoveButton')}
          </button>
        </div>
      )}
      {/* The CLI's refusal (in use in another profile, or by an org decider),
          next to the row; always rendered so it is announced when it appears. */}
      <div aria-live="assertive" data-testid={`jev-provider-remove-error-${p.name}`}>
        {removeErr && <div role="alert" style={errText}>{removeErr}</div>}
      </div>
      {/* Always rendered so a screen reader announces the result when it appears. */}
      <div aria-live="polite" data-testid={`jev-probe-${p.name}`}>
        {probe && !probe.running && (probe.ok ? (
          <div style={okText}>{t('settings.jev.providers.probeOk', { name: p.name, ms: probe.latency_ms ?? 0 })}</div>
        ) : (
          <div style={errText}>{t('settings.jev.providers.probeFailed', { name: p.name, error: testError(probe.error) })}</div>
        ))}
      </div>
    </div>
  )
}

// The consent shown before a provider on another host becomes the default.
function SwitchConfirm({ p, status, defaultName, busy, onCancel, onConfirm }) {
  const { t } = useTranslation()
  const host = hostOf(p.base_url) || p.base_url
  const following = (status?.surfaces || []).filter(s => s.enabled && !surfacePinned(s, defaultName))
  return (
    <div data-testid="jev-provider-switch-confirm" role="group" aria-label={t('settings.jev.providers.switchTitle', { name: p.name, host })} style={{
      background: 'rgba(234,179,8,.05)', border: '1px solid rgba(234,179,8,.22)', borderRadius: 'var(--radius)',
      padding: '10px 12px', display: 'flex', flexDirection: 'column', gap: 8,
    }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 6, fontFamily: mono, fontSize: 11, color: 'var(--yellow)' }}>
        <ShieldAlert size={13} /> {t('settings.jev.providers.switchTitle', { name: p.name, host })}
      </div>
      {following.length ? (
        <>
          <div style={hint}>{t('settings.jev.providers.switchFeatures', { host })}</div>
          {following.map(s => (
            <div key={s.surface} data-jev-switch-surface={s.surface} style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
              <span style={{ fontFamily: mono, fontSize: 11, color: 'var(--text)' }}>{s.title || s.surface}</span>
              <EgressList items={s.egress} />
            </div>
          ))}
        </>
      ) : (
        <div style={hint}>{t('settings.jev.providers.switchNone')}</div>
      )}
      <div style={hint}>{t('settings.jev.providers.switchOtherProfiles', { host })}</div>
      <div data-testid="jev-switch-deciders" style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
        <span style={{ fontFamily: mono, fontSize: 11, color: 'var(--text)' }}>{t('settings.jev.providers.switchDeciders')}</span>
        <EgressList items={[1, 2, 3].map(i => t(`settings.jev.providers.deciderEgress${i}`))} />
      </div>
      <div data-testid="jev-switch-nodes" style={hint}>{t('settings.jev.providers.switchNodes')}</div>
      <div data-testid="jev-switch-dynorg" style={hint}>{t('settings.jev.providers.switchDynorg')}</div>
      <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', flexWrap: 'wrap' }}>
        <button type="button" className="btn btn-secondary btn-sm" disabled={busy} onClick={onCancel}>{t('settings.jev.providers.cancel')}</button>
        <button type="button" className="btn btn-primary btn-sm" disabled={busy} onClick={onConfirm}>
          {busy ? t('settings.jev.providers.switching') : t('settings.jev.providers.switchConfirm', { host })}
        </button>
      </div>
    </div>
  )
}

// run(what, fn, onErr) is the section's one-call-at-a-time wrapper; reload
// reads the status and the providers again.
export default function JevProvidersBlock({ status, providers, providersErr, busy, run, reload }) {
  const { t } = useTranslation()
  const [probes, setProbes] = useState({})
  const [note, setNote] = useState('')
  const [err, setErr] = useState('')
  const [confirmRemove, setConfirmRemove] = useState('')
  const [removeErr, setRemoveErr] = useState({ name: '', text: '' })
  const [confirmSwitch, setConfirmSwitch] = useState('')
  const [adding, setAdding] = useState(false)

  const list = providers?.providers || []
  const defaultName = providers?.default || status?.provider || 'typesafe'
  const inUse = new Set((status?.surfaces || []).map(s => s.provider || '').filter(Boolean))
  const disabled = !!busy
  const start = () => { setErr(''); setNote(''); setRemoveErr({ name: '', text: '' }) }

  const doUse = (name) => run(`provider:use:${name}`, async () => {
    await JevUseProvider(name, '')
    setConfirmSwitch('')
    setNote(t('settings.jev.providers.switched', { name }))
    await reload()
  }, setErr)
  const use = (p) => {
    start(); setConfirmRemove('')
    if (isThisComputer(p.base_url)) doUse(p.name)
    else setConfirmSwitch(p.name)
  }
  const test = (name) => {
    start()
    setProbes(m => ({ ...m, [name]: { running: true } }))
    run(`provider:probe:${name}`, async () => {
      const r = await JevProbeProvider(name)
      setProbes(m => ({ ...m, [name]: r || { ok: false, error: '' } }))
    }, e => setProbes(m => ({ ...m, [name]: { ok: false, error: e } })))
  }
  const remove = (name) => {
    setRemoveErr({ name: '', text: '' })
    run(`provider:remove:${name}`, async () => {
      await JevRemoveProvider(name)
      setConfirmRemove('')
      setNote(t('settings.jev.providers.removed', { name }))
      await reload()
    }, e => { setConfirmRemove(''); setRemoveErr({ name, text: removeError(name, e, t) }) })
  }

  const switching = list.find(p => p.name === confirmSwitch)

  return (
    <div data-testid="jev-providers" style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
        <span style={label}>{t('settings.jev.providers.title')}</span>
        <span style={{ flex: 1 }} />
        {providers && !adding && (
          <button type="button" className="btn btn-secondary btn-sm" disabled={disabled} onClick={() => { start(); setAdding(true) }}>
            <Plus size={12} /> {t('settings.jev.providers.add')}
          </button>
        )}
      </div>
      <div style={hint}>{t('settings.jev.providers.intro')}</div>
      {status?.provider_error && (
        <div role="alert" data-testid="jev-provider-error" style={{ ...errText, display: 'flex', alignItems: 'flex-start', gap: 6 }}>
          <ShieldAlert size={13} style={{ flexShrink: 0, marginTop: 1 }} />
          <span>{t('settings.jev.providers.defaultUnusable', { name: status.provider || defaultName, error: status.provider_error.replace(/^jev:\s*/, '') })}</span>
        </div>
      )}
      {providersErr && !providers && (
        <div role="alert" style={errText}>{t('settings.jev.providers.loadErr', { error: providersErr })}</div>
      )}
      {list.map(p => (
        <ProviderRow
          key={p.name} p={p} isDefault={p.name === defaultName} inUse={inUse.has(p.name)} busy={disabled}
          probe={probes[p.name]} confirmRemove={confirmRemove === p.name}
          removeErr={removeErr.name === p.name ? removeErr.text : ''}
          onUse={() => use(p)} onTest={() => test(p.name)}
          onRemove={() => { start(); setConfirmSwitch(''); setConfirmRemove(p.name) }}
          onCancelRemove={() => setConfirmRemove('')} onConfirmRemove={() => remove(p.name)}
        />
      ))}
      {switching && (
        <SwitchConfirm p={switching} status={status} defaultName={defaultName} busy={disabled}
          onCancel={() => setConfirmSwitch('')} onConfirm={() => doUse(switching.name)} />
      )}
      {err && <div role="alert" style={errText}>{err}</div>}
      {note && <div style={okText}>{note}</div>}
      {list.length > 0 && <div style={hint}>{t('settings.jev.providers.calibrationNote')}</div>}
      {adding && (
        <JevAddProviderForm kinds={providers?.kinds || []} existing={list} busy={disabled} run={run} reload={reload} onClose={() => setAdding(false)} />
      )}
    </div>
  )
}
