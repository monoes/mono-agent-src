// Org header autonomy controls (U16/U17): level selector, decider picker, and
// Pause autonomy. Writes only through `monoagentcli org autonomy …` bindings;
// the level is enforced from the CLI's DB copy, so the bar always re-reads
// after a change instead of trusting local state.
import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { PauseCircle, PlayCircle, ShieldCheck, RefreshCw } from 'lucide-react'
import { api, notify } from '../../services/api.js'
import { LEVELS, DECIDERS, isPaused, effectiveLevel, JEV_THRESHOLD, parseJevThreshold } from './autonomyModel.js'
import { toMillis } from './waiting.js'
import FullAutoConfirm from './FullAutoConfirm.jsx'
import { Chip, mono, smallBtn } from './ui.jsx'

const PAUSE_OPTIONS = [
  { id: '30m', labelKey: 'orgs.autonomy.pauseFor30m' },
  { id: '2h', labelKey: 'orgs.autonomy.pauseFor2h' },
  { id: '', labelKey: 'orgs.autonomy.pauseUntilResumed' },
]

function pausedLabel(autonomy, t) {
  const until = toMillis(autonomy?.paused_until)
  if (until == null || until - Date.now() > 365 * 24 * 3600_000) return t('orgs.autonomy.paused')
  return t('orgs.autonomy.pausedUntil', { time: new Date(until).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) })
}

// JevThreshold is the jev decider's gate: Jev's top verdict must reach it or
// the model decider decides. Saved on blur / Enter through
// `org autonomy set --decider jev --decider-threshold X`.
function JevThreshold({ value, disabled, onSave }) {
  const { t } = useTranslation()
  const current = value ?? JEV_THRESHOLD
  const [draft, setDraft] = useState(String(current))
  useEffect(() => { setDraft(String(current)) }, [current])
  const commit = () => {
    const v = parseJevThreshold(draft)
    if (v == null) { setDraft(String(current)); return }
    setDraft(String(v))
    if (v !== current) onSave(v)
  }
  return (
    <label title={t('orgs.autonomy.jevThresholdHint')} style={{ display: 'flex', alignItems: 'center', gap: 4, ...mono, fontSize: 10, color: 'var(--text-muted)' }}>
      {t('orgs.autonomy.jevThreshold')}
      <input
        aria-label={t('orgs.autonomy.jevThreshold')}
        type="number" min={0.05} max={1} step={0.05}
        value={draft}
        disabled={disabled}
        onChange={e => setDraft(e.target.value)}
        onBlur={commit}
        onKeyDown={e => { if (e.key === 'Enter') e.currentTarget.blur() }}
        style={{
          ...mono, fontSize: 10, width: 52, padding: '2px 4px',
          background: 'var(--elevated)', color: 'var(--text)',
          border: '1px solid var(--border)', borderRadius: 'var(--radius)',
        }}
      />
    </label>
  )
}

// JevProviderSelect picks the decision provider the jev decider asks:
// "Default (<machine default>)" or a saved provider. Shown once a provider
// besides the built-in one exists, or when the org already names one.
function JevProviderSelect({ providers, value, disabled, onChange }) {
  const { t } = useTranslation()
  const list = providers?.providers || []
  if (!value && !list.some(p => !p.builtin)) return null
  const names = list.map(p => p.name)
  if (value && !names.includes(value)) names.push(value)
  return (
    <label title={t('orgs.autonomy.jevProviderHint')} style={{ display: 'flex', alignItems: 'center', gap: 4, ...mono, fontSize: 10, color: 'var(--text-muted)' }}>
      {t('orgs.autonomy.jevProvider')}
      <select
        aria-label={t('orgs.autonomy.jevProvider')}
        className="filter-select"
        value={value}
        disabled={disabled}
        onChange={e => onChange(e.target.value)}
        style={{ fontSize: 10, padding: '2px 22px 2px 6px' }}
      >
        <option value="">{t('orgs.autonomy.jevProviderDefault', { name: providers?.default || 'typesafe' })}</option>
        {names.map(n => <option key={n} value={n}>{n}</option>)}
      </select>
    </label>
  )
}

export default function AutonomyBar({ orgName, onChange, onOpenJevSettings }) {
  const { t } = useTranslation()
  const [autonomy, setAutonomy] = useState(null)
  const [loading, setLoading] = useState(true)
  const [unavailable, setUnavailable] = useState('')
  const [busy, setBusy] = useState(false)
  const [confirmFull, setConfirmFull] = useState(false)
  const [pauseOpen, setPauseOpen] = useState(false)
  const orgRef = useRef(orgName)
  orgRef.current = orgName
  const onChangeRef = useRef(onChange)
  onChangeRef.current = onChange

  const load = useCallback(async () => {
    if (!orgName) return
    const res = await api.getOrgAutonomy(orgName)
    if (orgRef.current !== orgName) return
    setLoading(false)
    if (!res || res.error) {
      setAutonomy(null)
      setUnavailable(res?.error || t('orgs.autonomy.unreadable'))
      return
    }
    setUnavailable('')
    setAutonomy(res)
    onChangeRef.current?.(res)
  }, [orgName, t])

  useEffect(() => {
    setLoading(true)
    setAutonomy(null)
    setPauseOpen(false)
    load()
    const iv = setInterval(load, 30_000)
    return () => clearInterval(iv)
  }, [load])

  // The saved decision providers, read only while the decider is Jev.
  const [providers, setProviders] = useState(null)
  const isJev = autonomy?.decider?.kind === 'jev'
  useEffect(() => {
    if (!isJev) return undefined
    let live = true
    Promise.resolve(api.jevProviders()).then(p => { if (live) setProviders(p || null) }).catch(() => {})
    return () => { live = false }
  }, [isJev])

  const apply = useCallback(async (fn, op) => {
    setBusy(true)
    try {
      const res = await fn()
      if (!res || res.error) notify(op, res?.error || `${op} failed`)
      await load()
    } finally {
      setBusy(false)
    }
  }, [load])

  const setLevel = (level) => {
    if (!autonomy || level === autonomy.level) return
    if (level === 'full') { setConfirmFull(true); return }
    apply(() => api.setOrgAutonomy(orgName, { level }), 'set autonomy level')
  }

  const setDecider = (kind) => {
    if (!autonomy || kind === autonomy.decider?.kind) return
    apply(() => api.setOrgAutonomy(orgName, { decider: { kind } }), 'set decider')
  }

  const setThreshold = (threshold) => {
    apply(() => api.setOrgAutonomy(orgName, { decider: { kind: 'jev', threshold } }), 'set jev threshold')
  }

  // '' puts the jev decider back on the machine's default provider.
  const setProvider = (provider) => {
    if (provider === (autonomy?.decider?.provider || '')) return
    apply(() => api.setOrgAutonomy(orgName, { decider: { kind: 'jev', provider } }), 'set jev provider')
  }

  if (!orgName) return null

  if (loading) {
    return <div style={{ ...mono, fontSize: 10, color: 'var(--text-muted)' }}>{t('orgs.autonomy.loading')}</div>
  }

  if (!autonomy) {
    return (
      <div style={{ display: 'flex', alignItems: 'center', gap: 6 }} title={unavailable}>
        <span style={{ ...mono, fontSize: 10, color: 'var(--text-muted)' }}>{t('orgs.autonomy.unavailable')}</span>
        <button style={smallBtn} onClick={() => { setLoading(true); load() }} aria-label={t('orgs.autonomy.retry')}>
          <RefreshCw size={10} />
        </button>
      </div>
    )
  }

  const paused = isPaused(autonomy)
  const eff = effectiveLevel(autonomy)
  const deciderKind = autonomy.decider?.kind || 'model'
  const fallbackModel = [autonomy.decider?.runtime, autonomy.decider?.model].filter(Boolean).join(' · ')

  return (
    <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'flex-end', gap: 8, flexWrap: 'wrap' }}>
      <ShieldCheck size={12} style={{ color: 'var(--text-muted)' }} />
      <div role="radiogroup" aria-label={t('orgs.autonomy.levelGroup')} style={{ display: 'flex', border: '1px solid var(--border)', borderRadius: 'var(--radius)', overflow: 'hidden' }}>
        {LEVELS.map(l => {
          const active = autonomy.level === l.id
          return (
            <button
              key={l.id}
              role="radio"
              aria-checked={active}
              disabled={busy}
              title={t(`orgs.autonomy.levels.${l.id}Hint`)}
              onClick={() => setLevel(l.id)}
              style={{
                ...mono, fontSize: 10, padding: '4px 9px', border: 'none', cursor: busy ? 'default' : 'pointer',
                background: active ? (l.id === 'full' ? 'rgba(239,68,68,0.15)' : 'var(--elevated)') : 'transparent',
                color: active ? (l.id === 'full' ? 'var(--red, #ef4444)' : 'var(--text)') : 'var(--text-muted)',
              }}
            >
              {t(`orgs.autonomy.levels.${l.id}`)}
            </button>
          )
        })}
      </div>

      <label style={{ display: 'flex', alignItems: 'center', gap: 4, ...mono, fontSize: 10, color: 'var(--text-muted)' }}>
        {t('orgs.autonomy.decider')}
        <select
          aria-label={t('orgs.autonomy.decider')}
          className="filter-select"
          value={deciderKind}
          disabled={busy}
          onChange={e => setDecider(e.target.value)}
          style={{ fontSize: 10, padding: '2px 22px 2px 6px' }}
        >
          {DECIDERS.map(d => <option key={d.id} value={d.id} title={t(`orgs.autonomy.deciders.${d.id}Hint`)}>{t(`orgs.autonomy.deciders.${d.id}`)}</option>)}
        </select>
      </label>

      {deciderKind === 'jev' && (
        <JevProviderSelect providers={providers} value={autonomy.decider?.provider || ''} disabled={busy} onChange={setProvider} />
      )}

      {deciderKind === 'jev' && (
        <JevThreshold value={autonomy.decider?.threshold} disabled={busy} onSave={setThreshold} />
      )}

      {paused ? (
        <>
          <Chip color="#eab308" title={t('orgs.autonomy.pausedTitle')}>{pausedLabel(autonomy, t)}</Chip>
          <button style={smallBtn} disabled={busy} onClick={() => apply(() => api.resumeOrgAutonomy(orgName), 'resume autonomy')}>
            <PlayCircle size={11} /> {t('orgs.autonomy.resume')}
          </button>
        </>
      ) : (
        <div style={{ position: 'relative' }}>
          <button
            style={smallBtn}
            disabled={busy || autonomy.level === 'manual'}
            title={autonomy.level === 'manual' ? t('orgs.autonomy.alreadyManual') : t('orgs.autonomy.pauseTitle')}
            aria-haspopup="menu"
            aria-expanded={pauseOpen}
            onClick={() => setPauseOpen(v => !v)}
          >
            <PauseCircle size={11} /> {t('orgs.autonomy.pause')}
          </button>
          {pauseOpen && (
            <div role="menu" style={{
              position: 'absolute', top: '100%', right: 0, marginTop: 4, zIndex: 20, minWidth: 140,
              background: 'var(--surface)', border: '1px solid var(--border-bright)', borderRadius: 'var(--radius)',
              boxShadow: '0 8px 24px rgba(0,0,0,0.5)', display: 'flex', flexDirection: 'column',
            }}>
              {PAUSE_OPTIONS.map(o => (
                <button
                  key={o.labelKey}
                  role="menuitem"
                  onClick={() => { setPauseOpen(false); apply(() => api.pauseOrgAutonomy(orgName, o.id), 'pause autonomy') }}
                  style={{ ...mono, fontSize: 10.5, textAlign: 'left', padding: '6px 10px', background: 'transparent', border: 'none', color: 'var(--text)', cursor: 'pointer' }}
                >
                  {t(o.labelKey)}
                </button>
              ))}
            </div>
          )}
        </div>
      )}

      {autonomy.daemon_running === false && autonomy.level !== 'manual' && (
        <Chip color="#eab308" title={t('orgs.autonomy.daemonOffTitle')}>{t('orgs.autonomy.daemonOff')}</Chip>
      )}
      {!paused && eff !== autonomy.level && autonomy.daemon_running !== false && (
        <Chip color="#eab308">{t('orgs.autonomy.actingAs', { level: t(`orgs.autonomy.levels.${eff}`, { defaultValue: eff }) })}</Chip>
      )}

      {deciderKind === 'jev' && (
        <div data-testid="jev-decider-note" style={{ flexBasis: '100%', display: 'flex', justifyContent: 'flex-end', gap: 4, flexWrap: 'wrap', ...mono, fontSize: 9.5, color: 'var(--text-muted)' }}>
          {(autonomy.decider?.provider || providers?.default || 'typesafe') === 'typesafe' && <span>{t('orgs.autonomy.jevNeedsKey')}</span>}
          {onOpenJevSettings ? (
            <button
              onClick={onOpenJevSettings}
              style={{ ...mono, fontSize: 9.5, padding: 0, background: 'none', border: 'none', color: 'var(--cyan, #00b4d8)', cursor: 'pointer', textDecoration: 'underline' }}
            >
              {t('orgs.autonomy.jevSettingsLink')}
            </button>
          ) : (
            <span style={{ color: 'var(--text-secondary)' }}>{t('orgs.autonomy.jevSettingsLink')}</span>
          )}
          <span>·</span>
          <span>{t(fallbackModel ? 'orgs.autonomy.jevFallbackModel' : 'orgs.autonomy.jevFallback', {
            model: fallbackModel, threshold: autonomy.decider?.threshold ?? JEV_THRESHOLD,
          })}</span>
        </div>
      )}

      <FullAutoConfirm
        open={confirmFull}
        orgName={orgName}
        autonomy={autonomy}
        deciderKind={deciderKind}
        onCancel={() => setConfirmFull(false)}
        onConfirm={() => { setConfirmFull(false); apply(() => api.setOrgAutonomy(orgName, { level: 'full' }), 'set autonomy level') }}
      />
    </div>
  )
}
