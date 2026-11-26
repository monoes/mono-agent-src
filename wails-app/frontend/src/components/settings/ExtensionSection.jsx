import { useState, useEffect, useCallback } from 'react'
import { useTranslation } from 'react-i18next'
import { GetExtensionInfo, InstallExtension } from '../../wailsjs/go/main/App'
import i18nOr from '../../lib/i18nOr.js'

// The browser extension is how monoagent drives a browser; there is no other
// way. It ships inside the app and is unpacked, on request, to a fixed folder
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

const errMsg = (e) => String(e?.message || e || i18nOr('ui6SetCoder.unknown_error'))

export default function ExtensionSection() {
  const { t } = useTranslation()
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
  const stale = info && (!info.installed || !info.current)

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(info.path)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    } catch { /* the path is selectable on screen */ }
  }

  return (
    <div style={card} data-testid="extension-section">
      <div style={label}>{t('ui6SetBrowser.ext_title')}</div>
      <div style={hint}>
        {t('ui6SetBrowser.ext_about')}
      </div>
      {error && <div style={errText} role="alert">{error}</div>}
      {info && (
        <>
          <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
            <code data-testid="extension-path" style={{ flex: 1, minWidth: 0, fontFamily: mono, fontSize: 11, color: 'var(--text)', wordBreak: 'break-all', userSelect: 'all' }}>
              {info.path}
            </code>
            <button style={button} onClick={copy}>{copied ? t('ui6SetBrowser.copied') : t('ui6SetBrowser.copy_path')}</button>
          </div>
          {stale && (
            <div data-testid="extension-stale" style={{ ...hint, color: 'var(--yellow)' }}>
              {info.installed
                ? t('ui6SetBrowser.ext_stale')
                : t('ui6SetBrowser.ext_missing')}
            </div>
          )}
          <ol style={{ ...hint, margin: 0, paddingLeft: 18 }}>
            <li>{t('ui6SetBrowser.step_open')} <code>chrome://extensions</code> (Edge: <code>edge://extensions</code>).</li>
            <li>{t('ui6SetBrowser.step_turn_on')} <strong>{t('ui6SetBrowser.developer_mode')}</strong> {t('ui6SetBrowser.step_top_right')}</li>
            <li>{t('ui6SetBrowser.step_click')} <strong>{t('ui6SetBrowser.load_unpacked')}</strong> {t('ui6SetBrowser.step_choose')}</li>
            <li>{t('ui6SetBrowser.step_pin')} <strong>MonoAgent Bridge</strong>, {t('ui6SetBrowser.step_pair')}</li>
          </ol>
          <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
            <span style={hint}>
              {info.version ? `${t('ui6SetBrowser.version', { version: info.version })} ` : ''}{t('ui6SetBrowser.after_update')} <strong>{t('ui6SetBrowser.reload')}</strong> {t('ui6SetBrowser.on_extension')}
            </span>
            <button style={{ ...button, marginLeft: 'auto' }} disabled={busy} onClick={() => run(InstallExtension)}>
              {busy ? t('ui6SetBrowser.working') : t(!info.installed ? 'ui6SetBrowser.install' : info.current ? 'ui6SetBrowser.reinstall' : 'ui6Set.status_update')}
            </button>
          </div>
        </>
      )}
    </div>
  )
}
