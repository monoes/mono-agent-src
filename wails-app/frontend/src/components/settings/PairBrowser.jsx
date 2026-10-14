import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { PairExtension } from '../../wailsjs/go/main/App'

// Pairing the browser extension from the app: `monoagentcli extension pair
// --open --json` (PairExtension in app_browsers.go) opens the running
// bridge's one-time pairing page, or says what to do instead. Used by
// Settings → Browsers and the dashboard's "Get started" step. Keys are
// written out in full for the key scan.

const hintStyle = { fontFamily: 'var(--font-body)', fontSize: 10.5, color: 'var(--text-muted)', lineHeight: 1.5 }
const urlStyle = { fontFamily: 'var(--font-mono)', fontSize: 10.5, color: 'var(--text)', wordBreak: 'break-all', userSelect: 'all' }

// parsePairResult turns the binding's JSON string into
// { opened, url, fallback, hint, error }; an unreadable answer is an error.
export function parsePairResult(raw) {
  try {
    const r = typeof raw === 'string' ? JSON.parse(raw) : raw
    if (r && typeof r === 'object') return r
  } catch { /* falls through */ }
  return { opened: false, error: String(raw || 'no answer') }
}

// withCode shows the CLI hint's `command` spans as code instead of
// literal backticks.
function withCode(text) {
  return String(text).split(/`([^`]+)`/).map((part, i) => (i % 2
    ? <code key={i} style={{ fontFamily: 'var(--font-mono)', color: 'var(--text)' }}>{part}</code>
    : part))
}

export function usePairExtension() {
  const [busy, setBusy] = useState(false)
  const [result, setResult] = useState(null)
  const pair = async () => {
    setBusy(true)
    try {
      setResult(parsePairResult(await PairExtension()))
    } catch (e) {
      setResult({ opened: false, error: String(e?.message || e) })
    } finally {
      setBusy(false)
    }
  }
  return { pair, busy, result }
}

export function PairButton({ pair, busy, className = 'btn btn-sm btn-secondary' }) {
  const { t } = useTranslation()
  return (
    <button className={className} onClick={pair} disabled={busy}>
      {busy ? t('settings.browsers.pairing') : t('settings.browsers.pair')}
    </button>
  )
}

// PairResult shows what the last pairing attempt did: the page opened, or
// the fallback (paste the token, restart the bridge) with the CLI's hint.
export function PairResult({ result }) {
  const { t } = useTranslation()
  if (!result) return null
  let headline = t('settings.browsers.pairNotOpened')
  if (result.opened) headline = t('settings.browsers.pairOpened')
  else if (result.fallback === 'restart_bridge') headline = t('settings.browsers.pairRestart')
  return (
    <div role="status" data-testid="pair-result" style={{ display: 'flex', flexDirection: 'column', gap: 4, marginTop: 6 }}>
      <div style={{ ...hintStyle, color: result.opened ? 'var(--text)' : 'var(--yellow)' }}>{headline}</div>
      {result.hint && <div style={hintStyle}>{withCode(result.hint)}</div>}
      {result.url && <div style={urlStyle}>{result.url}</div>}
      {!result.opened && result.error && (
        <details style={hintStyle}>
          <summary style={{ cursor: 'pointer' }}>{t('settings.browsers.details')}</summary>
          {result.error}
        </details>
      )}
    </div>
  )
}
