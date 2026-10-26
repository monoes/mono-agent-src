import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api } from '../../services/api.js'

const mono = 'var(--font-mono)'
const row = { display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap', padding: '6px 0', borderBottom: '1px solid var(--border)' }
const label = { fontFamily: mono, fontSize: 10, color: 'var(--text-muted)', minWidth: 78 }
const value = { fontFamily: mono, fontSize: 11, color: 'var(--text)' }
const INTERVALS = [0, 5, 10, 15, 30, 60]
const REFRESH_MS = 15000
const WHO = ['user', 'master', 'model']

// useSessionSettings reads a coder session's settings from the CLI and saves
// changes through it. save(patch) names only the settings to change; the CLI
// validates and answers with all of them, so what is shown is always what is stored.
function useSessionSettings(conversationId) {
  const { t } = useTranslation()
  // Read through a ref: the translation function changes identity when the language is set up or
  // switched, and that must not reload the settings and unmount the section.
  const tRef = useRef(t)
  tRef.current = t
  const [data, setData] = useState(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [rev, setRev] = useState(0) // bumps after each save, so a field shows what is stored, not what was refused
  // Bumped when a save starts and when it ends: a read that began before is older than what the save
  // stored, and its answer is dropped instead of putting the old values back.
  const epoch = useRef(0)
  useEffect(() => {
    let live = true
    setData(null); setError('')
    const read = () => {
      const began = epoch.current
      return api.getCoderSessionSettings(conversationId).then(r => { if (live && began === epoch.current) setData(r) })
    }
    read().catch(() => { if (live) setError(tRef.current('bubbles.pane.session.loadFailed')) })
    // Kept up to date while the pane is open: the check-in count changes by itself, and a message
    // from the person clears it. A refresh that fails keeps what is shown.
    const timer = setInterval(() => { read().catch(() => {}) }, REFRESH_MS)
    return () => { live = false; clearInterval(timer) }
  }, [conversationId])
  const save = useCallback(async (patch, unattended = '') => {
    epoch.current++
    setBusy(true); setError('')
    try {
      setData(await api.setCoderSessionSettings(conversationId, patch, unattended))
    } catch (err) {
      setError(tRef.current('bubbles.pane.session.saveFailed', { error: String(err?.message || err) }))
    } finally {
      epoch.current++
      setBusy(false)
      setRev(r => r + 1)
    }
  }, [conversationId])
  return { data, error, busy, save, rev }
}

const Err = ({ children }) => children ? <div role="alert" style={{ flexBasis: '100%', fontFamily: mono, fontSize: 10, color: 'var(--red)' }}>{children}</div> : null

// UnattendedRow is always visible in the pane: whether this chat keeps working
// with nobody here, and at which access level, so the person knows.
function UnattendedRow({ session: { data, error, busy, save }, access }) {
  const { t } = useTranslation()
  if (!data && !error) return null
  const on = data ? data.unattended !== false : true
  return (
    <div style={row} data-testid="pane-unattended">
      <span id="pane-unattended-label" style={label}>{t('bubbles.pane.session.unattended')}</span>
      <button type="button" role="switch" aria-checked={on} aria-labelledby="pane-unattended-label" disabled={busy || !data} onClick={() => save(null, on ? 'false' : 'true')}
        className="btn btn-ghost btn-sm" style={{ fontSize: 10, color: on ? '#fbbf24' : undefined }}>
        {on ? t('bubbles.pane.session.unattendedOn') : t('bubbles.pane.session.unattendedOff')}
        {access ? ` · ${t(`bubbles.pane.level.${access}`)}` : ''}
      </button>
      <span style={{ ...label, flexBasis: '100%', minWidth: 0 }}>{t('bubbles.pane.session.unattendedWhy')}</span>
      <div role="status" aria-live="polite" data-testid="pane-checkin-waiting" style={{ flexBasis: '100%', fontFamily: mono, fontSize: 10, color: '#fbbf24' }}>
        {data?.checkin?.waiting ? t('bubbles.pane.session.checkinWaiting', { count: data.checkin.streak }) : ''}
      </div>
      <Err>{error}</Err>
    </div>
  )
}

function Select({ id, value: v, options, onChange, disabled }) {
  return (
    <select id={id} value={v} disabled={disabled} onChange={e => onChange(e.target.value)} className="select-compact" style={{ minWidth: 110 }}>
      {options.map(o => <option key={o.value} value={o.value}>{o.label}</option>)}
    </select>
  )
}

// NumberField shows the stored value, or what the person is typing until they leave the field.
// There is no state copied from the stored value by an effect: such a copy runs on mount too, and
// there it can land after the first edit and write the stored value back over it. A refresh that
// brings another stored value does not touch a draft either; the field is replaced (by its key)
// once a save has finished, so it then shows what is stored.
function NumberField({ id, value: v, min, max, disabled, onCommit }) {
  const [draft, setDraft] = useState(null)
  const commit = () => {
    if (draft === null) return
    const n = Number(draft)
    if (draft.trim() !== '' && Number.isInteger(n) && n !== v) onCommit(n) // the draft stays on show while it saves
    else setDraft(null) // nothing to save (unchanged, empty or not a whole number): back to the stored value
  }
  return <input id={id} type="number" min={min} max={max} value={draft ?? String(v)} disabled={disabled} onChange={e => setDraft(e.target.value)} onBlur={commit}
    onKeyDown={e => { if (e.key === 'Enter') e.currentTarget.blur() }} className="input-compact" style={{ width: 56 }} />
}

// SessionSettings holds the knobs of one coder session: check-ins, autonomy,
// limits and mesh. They sit in a disclosure so the pane stays short.
export function SessionSettings({ conversationId, access }) {
  const { t } = useTranslation()
  const session = useSessionSettings(conversationId)
  const { data, error, busy, save, rev } = session
  if (!data) return error ? <UnattendedRow session={session} access={access} /> : null
  const s = data.settings
  const cur = s.checkin_minutes
  const intervals = INTERVALS.includes(cur) ? INTERVALS : [...INTERVALS, cur].sort((a, b) => a - b)
  const setClass = (cls, field, v) => {
    const classes = { ...s.autonomy_classes }
    if (v === '') delete classes[cls]
    else classes[cls] = { who: 'user', fallback: 'park', ...classes[cls], [field]: v }
    save({ autonomy_classes: classes })
  }
  return (
    <>
    <UnattendedRow session={session} access={access} />
    <details data-testid="pane-session" style={{ padding: '6px 0' }}>
      <summary style={{ ...label, cursor: 'pointer' }}>{t('bubbles.pane.session.title')}</summary>

      <div style={row} data-testid="pane-session-checkins">
        <label htmlFor="pane-checkin" style={label}>{t('bubbles.pane.session.checkinEvery')}</label>
        <Select id="pane-checkin" value={String(cur)} disabled={busy} onChange={v => save({ checkin_minutes: Number(v) })}
          options={intervals.map(m => ({ value: String(m), label: m === 0 ? t('bubbles.pane.session.checkinOff') : t('bubbles.pane.session.checkinMinutes', { count: m }) }))} />
        <label style={{ ...value, display: 'flex', gap: 4, alignItems: 'center' }}>
          <input type="checkbox" checked={s.skip_if_unchanged} disabled={busy} onChange={e => save({ skip_if_unchanged: e.target.checked })} />
          {t('bubbles.pane.session.skipUnchanged')}
        </label>
      </div>

      <div style={row} data-testid="pane-session-autonomy">
        <label htmlFor="pane-preset" style={label}>{t('bubbles.pane.session.preset')}</label>
        <Select id="pane-preset" value={s.autonomy_preset} disabled={busy} onChange={v => save({ autonomy_preset: v })}
          options={data.allowed.autonomy_preset.map(p => ({ value: p, label: t(`bubbles.pane.session.presetOptions.${p}`) }))} />
        <span style={{ ...label, flexBasis: '100%', minWidth: 0 }}>{t('bubbles.pane.session.classes')}</span>
        {data.allowed.gap_classes.map(cls => {
          const c = s.autonomy_classes[cls]
          return (
            <div key={cls} style={{ display: 'flex', gap: 6, alignItems: 'center', flexBasis: '100%' }}>
              <span style={{ ...value, minWidth: 120 }}>{t(`bubbles.pane.session.class.${cls}`)}</span>
              <select aria-label={t(`bubbles.pane.session.class.${cls}`)} value={c ? c.who : ''} disabled={busy} onChange={e => setClass(cls, 'who', e.target.value)} className="select-compact">
                <option value="">{t('bubbles.pane.session.inherit')}</option>
                {WHO.map(w => <option key={w} value={w}>{t(`bubbles.pane.session.who.${w}`)}</option>)}
              </select>
              {c && (
                <select aria-label={`${t(`bubbles.pane.session.class.${cls}`)} – fallback`} value={c.fallback} disabled={busy} onChange={e => setClass(cls, 'fallback', e.target.value)} className="select-compact">
                  {['park', 'take_default'].map(f => <option key={f} value={f}>{t(`bubbles.pane.session.fallback.${f}`)}</option>)}
                </select>
              )}
            </div>
          )
        })}
      </div>

      <div style={row} data-testid="pane-session-limits">
        <label htmlFor="pane-max-children" style={label}>{t('bubbles.pane.session.maxChildren')}</label>
        <NumberField key={`mc${rev}`} id="pane-max-children" value={s.max_children} min={0} max={20} disabled={busy} onCommit={n => save({ max_children: n })} />
        <label htmlFor="pane-max-concurrent" style={label}>{t('bubbles.pane.session.maxConcurrent')}</label>
        <NumberField key={`mx${rev}`} id="pane-max-concurrent" value={s.max_concurrent} min={1} max={10} disabled={busy} onCommit={n => save({ max_concurrent: n })} />
        <span style={value}>{t('bubbles.pane.session.depth', { depth: s.max_depth })}</span>
        <label htmlFor="pane-checkin-limit" style={label}>{t('bubbles.pane.session.checkinLimit')}</label>
        <NumberField key={`cl${rev}`} id="pane-checkin-limit" value={s.checkin_limit} min={1} max={500} disabled={busy} onCommit={n => save({ checkin_limit: n })} />
        <span style={{ ...label, flexBasis: '100%', minWidth: 0 }}>{t('bubbles.pane.session.checkinLimitWhy')}</span>
      </div>

      <div style={row} data-testid="pane-session-mesh">
        <label style={{ ...value, display: 'flex', gap: 4, alignItems: 'center' }}>
          <input type="checkbox" checked={s.mesh_visible} disabled={busy} onChange={e => save({ mesh_visible: e.target.checked })} />
          {t('bubbles.pane.session.meshVisible')}
        </label>
        <label htmlFor="pane-mesh-alias" style={label}>{t('bubbles.pane.session.meshAlias')}</label>
        <input id="pane-mesh-alias" defaultValue={s.mesh_alias} key={`al${rev}`} disabled={busy} className="input-compact" style={{ width: 110 }} maxLength={64}
          onBlur={e => { if (e.target.value !== s.mesh_alias) save({ mesh_alias: e.target.value }) }} />
        <label htmlFor="pane-mesh-accept" style={label}>{t('bubbles.pane.session.meshAccept')}</label>
        <Select id="pane-mesh-accept" value={s.mesh_accept} disabled={busy} onChange={v => save({ mesh_accept: v })}
          options={data.allowed.mesh_accept.map(a => ({ value: a, label: t(`bubbles.pane.session.accept.${a}`) }))} />
      </div>
    </details>
    </>
  )
}
