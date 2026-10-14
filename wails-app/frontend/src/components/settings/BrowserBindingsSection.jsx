import { useState, useEffect, useCallback, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { GetBrowsers, BindBrowser, GetProfiles, GetExtensionStatus } from '../../wailsjs/go/main/App'
import { bridgeState } from '../../pages/dashboard/getStarted.js'
import { usePairExtension, PairButton, PairResult } from './PairBrowser.jsx'

// Which browser each profile's automations run in. Everything goes through
// `monoagentcli extension browsers|bind|unbind` (app_browsers.go). The
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

const errMsg = (e) => String(e?.message || e || 'unknown error')

// unpaired: whether `extension status` says the bridge turns this client or
// the extension away. Asked only when the browser list failed or came back
// empty, so a working setup costs no extra CLI call.
async function readUnpaired() {
  try {
    return bridgeState(null, JSON.parse(await GetExtensionStatus())) === 'unpaired'
  } catch {
    return false
  }
}

export default function BrowserBindingsSection() {
  const [report, setReport] = useState(null)
  const [profiles, setProfiles] = useState([])
  const [error, setError] = useState('')
  const [busy, setBusy] = useState('')
  const [unpaired, setUnpaired] = useState(false)
  const { t } = useTranslation()
  const pairing = usePairExtension()

  const load = useCallback(async () => {
    try {
      const [r, p] = await Promise.all([GetBrowsers(), GetProfiles()])
      setReport(r)
      setProfiles(p || [])
      setError('')
      setUnpaired(r?.running && !(r.browsers || []).length ? await readUnpaired() : false)
    } catch (e) {
      setError(errMsg(e))
      setUnpaired(await readUnpaired())
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

  const bind = async (instance, profileId) => {
    setBusy(instance)
    try {
      await BindBrowser(instance, profileId)
      await load()
    } catch (e) {
      setError(errMsg(e))
    } finally {
      setBusy('')
    }
  }

  const browsers = report?.browsers || []
  return (
    <div ref={cardRef} style={card} data-testid="browser-bindings">
      <div style={label}>Browsers</div>
      <div style={hint}>
        Each browser profile with the MonoAgent Bridge extension can run one profile's automations,
        signed in as that browser's accounts. Profiles bound to different browsers run side by side.
      </div>
      {unpaired && (
        <div data-testid="browser-unpaired" style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
            <div style={{ flex: 1, minWidth: 0 }}>
              <div style={name}>{t('settings.browsers.notPaired')}</div>
              <div style={hint}>{t('settings.browsers.notPairedWhy')}</div>
            </div>
            <PairButton pair={pairing.pair} busy={pairing.busy} />
          </div>
          <PairResult result={pairing.result} />
          {error && (
            <details style={hint}>
              <summary style={{ cursor: 'pointer' }}>{t('settings.browsers.details')}</summary>
              <span style={{ fontFamily: mono }}>{error}</span>
            </details>
          )}
        </div>
      )}
      {error && !unpaired && <div style={errText} role="alert">{error}</div>}
      {report && !report.running && <div style={hint}>{report.hint || 'No bridge is running.'}</div>}
      {report?.running && report.hint && <div style={warnText}>{report.hint}</div>}
      {report?.running && !report.hint && !unpaired && browsers.length === 0 && (
        <div style={hint}>No browser is attached. Open a browser that has the MonoAgent Bridge extension.</div>
      )}
      {browsers.map((b) => {
        const title = b.label || b.instance.slice(0, 8)
        return (
          <div key={b.instance} data-browser={b.instance} style={row}>
            <div style={{ flex: 1, minWidth: 0 }}>
              <div style={name}>{title}</div>
              <div style={hint}>{b.instance.slice(0, 8)}{b.version ? ` · extension ${b.version}` : ''}</div>
              {b.conflict && <div style={warnText}>Another browser is bound to the same profile — only the most recently connected one is used.</div>}
              {b.legacy && <div style={warnText}>This browser's extension is too old to bind. Reload it from the browser's extensions page.</div>}
            </div>
            <select
              aria-label={`Profile for ${b.label || b.instance}`}
              value={b.profile_id || ''}
              disabled={b.legacy || busy === b.instance}
              onChange={(e) => bind(b.instance, e.target.value)}
              style={selectStyle}
            >
              <option value="">Any profile (default browser)</option>
              {profiles.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
              {b.profile_id && !profiles.some((p) => p.id === b.profile_id) && (
                <option value={b.profile_id}>{b.profile_id} (unknown)</option>
              )}
            </select>
          </div>
        )
      })}
    </div>
  )
}
