import { useState, useEffect, useCallback, useRef } from 'react'
import { GetBrowsers, AssignBrowser, GetProfiles } from '../../wailsjs/go/main/App'

// Which browser each MonoAgent profile's automations run in. One row per
// profile; any number of profiles may pick the same browser. Everything goes
// through `monoagentcli extension browsers|assign` (app_browsers.go). The
// binding lives in each browser's extension; this only asks it to change.

const mono = 'var(--font-mono)'
const card = {
  background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius-lg)',
  padding: '16px 20px', display: 'flex', flexDirection: 'column', gap: 14, marginBottom: 16,
}
const label = { fontFamily: mono, fontSize: 10, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: 1 }
const hint = { fontFamily: 'var(--font-body)', fontSize: 10.5, color: 'var(--text-muted)', lineHeight: 1.5 }
const errText = { fontFamily: mono, fontSize: 10.5, color: 'var(--red)', lineHeight: 1.5, wordBreak: 'break-word' }
const warnText = { fontFamily: 'var(--font-body)', fontSize: 10.5, color: 'var(--yellow)', lineHeight: 1.5 }
const row = { display: 'flex', alignItems: 'center', gap: 12, borderTop: '1px solid var(--border)', paddingTop: 10 }
const name = { fontFamily: 'var(--font-body)', fontSize: 12.5, color: 'var(--text)' }
// The look comes from the global `select` rule in index.css (dark,
// cyan chevron, dimmed when disabled); only layout goes inline.
const selectStyle = { minWidth: 180 }

export const BROWSERS_POLL_MS = 10000

// The profiles a browser runs. An older CLI reports only the first one.
const profileIdsOf = (b) => b.profile_ids || (b.profile_id ? [b.profile_id] : [])
const browserTitle = (b) => `${b.label || 'Browser'} · ${b.instance.slice(0, 8)}`

const errMsg = (e) => String(e?.message || e || 'unknown error')

export default function BrowserBindingsSection() {
  const [report, setReport] = useState(null)
  const [profiles, setProfiles] = useState([])
  const [error, setError] = useState('')
  const [busy, setBusy] = useState('')

  const load = useCallback(async () => {
    try {
      const [r, p] = await Promise.all([GetBrowsers(), GetProfiles()])
      setReport(r)
      setProfiles(p || [])
      setError('')
    } catch (e) {
      setError(errMsg(e))
    }
  }, [])
  useEffect(() => { load() }, [load])

  // A binding can change outside this section (the extension's side panel,
  // `monoagentcli extension bind`). Re-read on window focus, and every
  // 10 s while the section is actually on screen: the app keeps pages
  // mounted behind display:none, where polling would only spawn CLIs.
  const cardRef = useRef(null)
  useEffect(() => {
    const onFocus = () => load()
    window.addEventListener('focus', onFocus)
    const id = setInterval(() => { if (cardRef.current?.offsetParent) load() }, BROWSERS_POLL_MS)
    return () => { window.removeEventListener('focus', onFocus); clearInterval(id) }
  }, [load])

  const assign = async (profileId, instance) => {
    setBusy(profileId)
    try {
      await AssignBrowser(profileId, instance)
      await load()
    } catch (e) {
      setError(errMsg(e))
    } finally {
      setBusy('')
    }
  }

  const browsers = report?.browsers || []
  // The browser a profile shows as: the first (newest) that runs it.
  const runners = (profileId) => browsers.filter((b) => profileIdsOf(b).includes(profileId))
  return (
    <div ref={cardRef} style={card} data-testid="browser-bindings">
      <div style={label}>Browsers</div>
      <div style={hint}>
        Profiles can share a browser; they then share that browser's logged-in accounts. Profiles on
        different browsers run side by side.
      </div>
      {error && <div style={errText} role="alert">{error}</div>}
      {report && !report.running && <div style={hint}>{report.hint || 'No bridge is running.'}</div>}
      {report?.running && report.hint && <div style={warnText}>{report.hint}</div>}
      {report?.running && !report.hint && browsers.length === 0 && (
        <div style={hint}>No browser is attached. Open a browser that has the MonoAgent Bridge extension.</div>
      )}
      {profiles.map((p) => {
        const using = runners(p.id)
        return (
          <div key={p.id} data-profile={p.id} style={row}>
            <div style={{ flex: 1, minWidth: 0 }}>
              <div style={name}>{p.name}{p.is_active ? ' (active)' : ''}</div>
              {using.length > 1 && (
                <div style={warnText}>More than one browser runs this profile — only the most recently connected one is used.</div>
              )}
            </div>
            <select
              aria-label={`Browser for ${p.name}`}
              value={using[0]?.instance || ''}
              disabled={busy === p.id || browsers.length === 0}
              onChange={(e) => assign(p.id, e.target.value)}
              style={selectStyle}
            >
              <option value="">Any browser (default)</option>
              {browsers.map((b) => (
                <option key={b.instance} value={b.instance} disabled={b.legacy}>
                  {browserTitle(b)}{b.legacy ? ' (too old to bind)' : ''}
                </option>
              ))}
            </select>
          </div>
        )
      })}
      {browsers.length > 0 && (
        <div style={{ ...row, flexDirection: 'column', alignItems: 'stretch', gap: 4 }}>
          {browsers.map((b) => {
            const n = profileIdsOf(b).length
            return (
              <div key={b.instance} data-browser={b.instance} style={hint}>
                {browserTitle(b)}{b.version ? ` · extension ${b.version}` : ''}
                {': '}
                {b.legacy
                  ? "this browser's extension is too old to bind. Reload it from the browser's extensions page."
                  : n === 0
                    ? 'no profiles of its own (the default browser)'
                    : `${n} ${n === 1 ? 'profile uses' : 'profiles use'} this browser`}
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}
