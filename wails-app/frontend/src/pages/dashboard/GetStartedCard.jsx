import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { CheckCircle2, Circle, Rocket, X } from 'lucide-react'
import { SignInDialog } from '../../components/account/AccountBanners.jsx'
import { usePairExtension, PairButton, PairResult } from '../../components/settings/PairBrowser.jsx'

// "Get started": the first-run checklist at the top of the dashboard. Each
// step shows its state and one action; the steps come from getStartedSteps
// (getStarted.js). Keys are written out in full for the key scan.
const COPY = {
  signIn: { title: 'dashboard.getStarted.signIn', why: 'dashboard.getStarted.signInWhy', action: 'dashboard.getStarted.signInAction' },
  firstRun: { title: 'dashboard.getStarted.firstRun', why: 'dashboard.getStarted.firstRunWhy', action: 'dashboard.getStarted.firstRunAction' },
  extension: { title: 'dashboard.getStarted.extension', why: 'dashboard.getStarted.extensionWhy', action: 'dashboard.getStarted.extensionAction' },
  daemon: { title: 'dashboard.getStarted.daemon', why: 'dashboard.getStarted.daemonWhy', action: 'dashboard.getStarted.daemonAction' },
}

export default function GetStartedCard({ steps, bridge, onNavigate, onSignedIn, onDismiss }) {
  const { t } = useTranslation()
  const [signingIn, setSigningIn] = useState(false)
  // An unpaired bridge is one click from working: pair from here rather
  // than sending the user to Settings.
  const pairing = usePairExtension()
  const actions = {
    signIn: () => setSigningIn(true),
    firstRun: () => onNavigate('noderunner', { openTemplates: true }),
    extension: () => onNavigate('settings', { section: 'health' }),
    daemon: () => onNavigate('settings', { section: 'health' }),
  }
  const done = steps.filter(s => s.done).length
  return (
    <div className="card" data-testid="get-started" style={{ marginBottom: 16 }}>
      <div className="section-header">
        <div className="section-title"><Rocket size={12} /> {t('dashboard.getStarted.title')}</div>
        <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10.5, color: 'var(--text-muted)', marginLeft: 'auto', marginRight: 6 }}>
          {t('dashboard.getStarted.progress', { done, total: steps.length })}
        </span>
        <button className="btn btn-ghost btn-icon" onClick={onDismiss} aria-label={t('dashboard.getStarted.dismiss')} title={t('dashboard.getStarted.dismiss')}>
          <X size={13} />
        </button>
      </div>
      <ol style={{ listStyle: 'none', margin: 0, padding: 0, display: 'flex', flexDirection: 'column', gap: 2 }}>
        {steps.map(step => {
          const copy = COPY[step.id]
          const pairHere = step.id === 'extension' && bridge === 'unpaired'
          const why = pairHere ? t('dashboard.getStarted.extensionWhyUnpaired') : t(copy.why)
          return (
            <li key={step.id} style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '8px 4px', borderTop: '1px solid var(--border-dim)' }}>
              {step.done
                ? <CheckCircle2 size={15} aria-hidden="true" style={{ color: 'var(--green-neon)', flexShrink: 0 }} />
                : <Circle size={15} aria-hidden="true" style={{ color: 'var(--text-dim)', flexShrink: 0 }} />}
              <div style={{ flex: 1, minWidth: 0 }}>
                <div style={{ fontSize: 12.5, color: step.done ? 'var(--text-muted)' : 'var(--text)', textDecoration: step.done ? 'line-through' : 'none' }}>
                  {t(copy.title)}
                  {step.optional && <span style={{ marginLeft: 6, fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)' }}>{t('dashboard.getStarted.optional')}</span>}
                </div>
                {!step.done && <div style={{ fontSize: 11, color: 'var(--text-muted)', marginTop: 2 }}>{why}</div>}
                {!step.done && pairHere && <PairResult result={pairing.result} />}
              </div>
              {step.done
                ? <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10.5, color: 'var(--green-neon)' }}>{t('dashboard.getStarted.done')}</span>
                : pairHere
                  ? <PairButton pair={pairing.pair} busy={pairing.busy} />
                  : <button className={`btn btn-sm ${step.optional ? 'btn-ghost' : 'btn-secondary'}`} onClick={actions[step.id]}>{t(copy.action)}</button>}
            </li>
          )
        })}
      </ol>
      {signingIn && <SignInDialog onClose={() => setSigningIn(false)} onSignedIn={() => { setSigningIn(false); onSignedIn?.() }} />}
    </div>
  )
}
