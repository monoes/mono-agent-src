import { useState, useEffect, useCallback } from 'react'
import { GetExtensionInfo, InstallExtension } from '../../wailsjs/go/main/App'

// The browser extension is how monoagent drives a browser; there is no other
// way. It ships inside the app and is unpacked to a fixed folder
// (`monoagentcli extension info|install`, app_extension_install.go), which
// the browser loads once with "Load unpacked". This shows that folder and
// the steps.

const mono = 'var(--font-mono)'
const card = {
  background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius-lg)',
  padding: '16px 20px', display: 'flex', flexDirection: 'column', gap: 12, marginBottom: 16,
}
const label = { fontFamily: mono, fontSize: 10, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: 1 }
const hint = { fontFamily: 'var(--font-body)', fontSize: 11, color: 'var(--text-muted)', lineHeight: 1.6 }
const errText = { fontFamily: mono, fontSize: 10.5, color: 'var(--red)', lineHeight: 1.5, wordBreak: 'break-word' }
const button = { fontFamily: mono, fontSize: 10, padding: '5px 12px', borderRadius: 5, border: '1px solid var(--border)', background: 'transparent', color: 'var(--text-secondary)', cursor: 'pointer' }

const errMsg = (e) => String(e?.message || e || 'unknown error')

export default function ExtensionSection() {
  const [info, setInfo] = useState(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [copied, setCopied] = useState(false)

  const run = useCallback(async (fn) => {
    setBusy(true)
    try {
      setInfo(await fn())
      setError('')
    } catch (e) {
      setError(errMsg(e))
    } finally {
      setBusy(false)
    }
  }, [])
  useEffect(() => { run(GetExtensionInfo) }, [run])

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(info.path)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    } catch { /* the path is selectable on screen */ }
  }

  return (
    <div style={card} data-testid="extension-section">
      <div style={label}>MonoAgent Bridge extension</div>
      <div style={hint}>
        monoagent browses through this extension, in your own browser and signed in as you. It
        never starts a browser of its own. The full extension comes with the app: load it in
        Chrome or Edge once, as an unpacked extension.
      </div>
      {error && <div style={errText} role="alert">{error}</div>}
      {info && (
        <>
          <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
            <code data-testid="extension-path" style={{ flex: 1, minWidth: 0, fontFamily: mono, fontSize: 11, color: 'var(--text)', wordBreak: 'break-all', userSelect: 'all' }}>
              {info.path}
            </code>
            <button style={button} onClick={copy}>{copied ? 'Copied' : 'Copy path'}</button>
          </div>
          <ol style={{ ...hint, margin: 0, paddingLeft: 18 }}>
            <li>Open <code>chrome://extensions</code> (Edge: <code>edge://extensions</code>).</li>
            <li>Turn on <strong>Developer mode</strong> (top right).</li>
            <li>Click <strong>Load unpacked</strong> and choose the folder above.</li>
            <li>Pin <strong>MonoAgent Bridge</strong>, open its icon and pair it with the app.</li>
          </ol>
          <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
            <span style={hint}>
              {info.version ? `Version ${info.version}. ` : ''}After MonoAgent updates, reinstall and click <strong>Reload</strong> on the extension.
            </span>
            <button style={{ ...button, marginLeft: 'auto' }} disabled={busy} onClick={() => run(InstallExtension)}>
              {busy ? 'Working…' : 'Reinstall'}
            </button>
          </div>
        </>
      )}
    </div>
  )
}
