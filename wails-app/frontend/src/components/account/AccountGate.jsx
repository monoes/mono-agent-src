// What the app shows instead of itself while the machine's monoes.me sign-in
// does not allow work (spec §6.5): why, and what can be done about it. A locked
// status offers the sign-in controls the library gate uses; a failed or
// unreadable `account status` (a CLI that is missing, too old or not answering)
// names the cause and offers the app update. The keys are written out in full
// so locales/accountKeys.test.js can scan them.
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Download, RefreshCw } from 'lucide-react'
import LogInToMonoesButton from '../library/LogInToMonoesButton.jsx'
import { AppSelfUpdate, QuitApp } from '../../wailsjs/go/main/App'
import UpdateNotice from '../UpdateNotice.jsx'
import { subscribeEvent } from '../../services/api.js'
import { account, onAccountLogin } from '../../services/account.js'

// signIn: show the sign-in controls; update: offer the app update; retry: ask again;
// noUpdate: never offer the update (AppSelfUpdate installs by running monoagentcli).
const CLOCK = { title: 'account.gate.clock.title', body: 'account.gate.clock.body', signIn: true }
const REASONS = {
  not_logged_in: { title: 'account.gate.notLoggedIn.title', body: 'account.gate.notLoggedIn.body', signIn: true },
  expired: { title: 'account.gate.expired.title', body: 'account.gate.expired.body', signIn: true },
  refused: { title: 'account.gate.refused.title', body: 'account.gate.refused.body', signIn: true },
  clock_rollback: CLOCK,
  clock_skew: CLOCK,
  key_unknown: { title: 'account.gate.keyUnknown.title', body: 'account.gate.keyUnknown.body', update: true },
  // This computer dropped its saved sign-in because a refresh may have been lost (A24): signing in again here is the way out.
  unconfirmed: { title: 'account.gate.unconfirmed.title', body: 'account.gate.unconfirmed.body', signIn: true },
  // Also the words for a reason this app does not know (a newer monoagentcli may report one).
  invalid: { title: 'account.gate.invalid.title', body: 'account.gate.invalid.body', signIn: true },
}
const CAUSES = {
  cli_not_found: { title: 'account.gate.cliNotFound.title', body: 'account.gate.cliNotFound.body', retry: true, noUpdate: true },
  cli_too_old: { title: 'account.gate.cliTooOld.title', body: 'account.gate.cliTooOld.body', update: true, retry: true },
  cli_failed: { title: 'account.gate.cliFailed.title', body: 'account.gate.cliFailed.body', update: true, retry: true },
}

const mono = { fontFamily: 'var(--font-mono)', fontSize: 11 }
const SIGNED_OUT = { logged_in: false }

function UpdateControl({ version }) {
  const { t } = useTranslation()
  const [state, setState] = useState({ phase: 'idle' }) // idle | working | done | upToDate | error
  useEffect(() => subscribeEvent('update:progress', msg => setState(s => (s.phase === 'working' ? { ...s, progress: msg } : s))), [])

  const run = async () => {
    setState({ phase: 'working', progress: '' })
    try {
      const r = await AppSelfUpdate()
      if (r?.success) setState({ phase: r.up_to_date ? 'upToDate' : 'done', notice: r.notice })
      else setState({ phase: 'error', message: r?.error || '' })
    } catch (e) {
      setState({ phase: 'error', message: e?.message || String(e) })
    }
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 6 }}>
      {version && state.phase === 'idle' && <span style={{ ...mono, color: 'var(--text-secondary)' }}>{t('account.update.available', { version })}</span>}
      <button className="btn btn-primary" onClick={run} disabled={state.phase === 'working'} style={{ gap: 8 }}>
        <Download size={14} /> {state.phase === 'working' ? t('account.update.working') : t('account.update.button')}
      </button>
      {state.phase === 'working' && state.progress && <span style={{ ...mono, color: 'var(--text-secondary)' }}>{state.progress}</span>}
      {state.phase === 'done' && <span role="status" style={{ ...mono, color: 'var(--teal)' }}>{t('account.update.done')}</span>}
      {state.phase === 'done' && state.notice && (
        <UpdateNotice notice={state.notice} onRestart={() => QuitApp()} onLater={() => setState(s => ({ ...s, notice: '' }))} />
      )}
      {state.phase === 'upToDate' && <span role="status" style={{ ...mono, color: 'var(--text-secondary)' }}>{t('account.update.upToDate')}</span>}
      {state.phase === 'error' && <span role="alert" style={{ ...mono, color: 'var(--red)', maxWidth: 380 }}>{t('account.update.failed', { message: state.message })}</span>}
    </div>
  )
}

export default function AccountGate({ gate, view, update }) {
  const { t } = useTranslation()
  const [asking, setAsking] = useState(false)
  const [showDetails, setShowDetails] = useState(false)
  const what = view.failure ? CAUSES[view.failure.cause] : (REASONS[view.status?.reason] ?? REASONS.invalid)
  const message = view.failure?.message || ''

  const tryAgain = async () => {
    setAsking(true)
    try { await gate.check('manual') } finally { setAsking(false) }
  }

  return (
    <div style={{ width: '100vw', height: '100vh', display: 'flex', alignItems: 'center', justifyContent: 'center', background: 'var(--void)', position: 'relative' }}>
      {/* The window has no title bar of its own: this strip is how it is moved. */}
      <div aria-hidden="true" style={{ position: 'absolute', top: 0, left: 0, right: 0, height: 36, WebkitAppRegion: 'drag' }} />
      <div role="region" aria-labelledby="account-gate-title"
        style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', textAlign: 'center', gap: 14, maxWidth: 460, padding: 24 }}>
        <div className="logo-mark" aria-hidden="true" style={{ width: 40, height: 40, fontSize: 18 }}>M</div>
        <h1 id="account-gate-title" style={{ margin: 0, fontFamily: 'var(--font-display)', fontSize: 20, fontWeight: 700, color: 'var(--text)' }}>
          {t(what.title)}
        </h1>
        <p style={{ margin: 0, fontFamily: 'var(--font-body)', fontSize: 13, lineHeight: 1.55, color: 'var(--text-secondary)' }}>
          {t(what.body)}
        </p>
        {message && (
          <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 6 }}>
            <button className="btn btn-ghost btn-sm" aria-expanded={showDetails} aria-controls="account-gate-details" onClick={() => setShowDetails(v => !v)}>
              {t('account.gate.details')}
            </button>
            {showDetails && <p id="account-gate-details" style={{ ...mono, margin: 0, color: 'var(--text-muted)', wordBreak: 'break-word' }}>{message}</p>}
          </div>
        )}
        {what.signIn && <LogInToMonoesButton service={account} onLogin={onAccountLogin} status={SIGNED_OUT} onStatusChange={() => gate.check('manual')} large />}
        {(what.update || update) && !what.noUpdate && <UpdateControl version={update?.latest_version} />}
        {what.retry && (
          <button className="btn btn-ghost btn-sm" onClick={tryAgain} disabled={asking} style={{ gap: 6 }}>
            <RefreshCw size={11} /> {t('account.gate.retry')}
          </button>
        )}
      </div>
    </div>
  )
}
