// Step 5, Turn on monomind: two explainers (documents, orgs) and one button
// that runs InitializeMonomindProfile for the active profile. The run and its
// streamed lines live in ../monomindInit.js, so Skip and Back stay usable
// while it runs and a result that lands later still reloads the state. The
// step's status is derived on the Go side (IsMonomindInitialized), so this
// step writes no mark of its own.
import { useEffect, useState, useSyncExternalStore } from 'react'
import { useTranslation } from 'react-i18next'
import * as GoApp from '../../wailsjs/go/main/App'
import { callStep } from '../callStep.js'
import { getMonomindInit, startMonomindInit, subscribeMonomindInit } from '../monomindInit.js'
import './Monomind.css'

const Pin = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <path d="M12 21s-6-5.3-6-10a6 6 0 0 1 12 0c0 4.7-6 10-6 10z" /><circle cx="12" cy="11" r="2.2" />
  </svg>
)

const Check = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.4" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <path d="M5 12.5l4.5 4.5L19 7" />
  </svg>
)

const MiniDocs = () => (
  <svg className="onb-mm-mini" viewBox="0 0 220 92" aria-hidden="true">
    <g fill="oklch(0.30 0.05 290)" stroke="oklch(0.97 0.01 290 / .3)">
      <rect x="12" y="14" width="34" height="44" rx="4" /><rect x="22" y="22" width="34" height="44" rx="4" /><rect x="32" y="30" width="34" height="44" rx="4" />
    </g>
    <g stroke="oklch(0.97 0.01 290 / .5)" strokeWidth="2"><path d="M38 44h20M38 52h20M38 60h14" /></g>
    <path d="M74 52h22" stroke="var(--onb-accent)" strokeWidth="2" strokeDasharray="3 4" />
    <g stroke="oklch(0.83 0.15 72 / .7)" strokeWidth="1.5" fill="none"><path d="M120 30L160 22L196 46L170 72L130 66Z M160 22L170 72 M120 30L196 46" /></g>
    <g fill="var(--onb-accent)">
      <circle cx="120" cy="30" r="6" /><circle cx="160" cy="22" r="5" /><circle cx="196" cy="46" r="6" /><circle cx="170" cy="72" r="5" /><circle cx="130" cy="66" r="4" />
    </g>
  </svg>
)

const MiniOrg = () => (
  <svg className="onb-mm-mini" viewBox="0 0 220 92" aria-hidden="true">
    <g stroke="oklch(0.97 0.01 290 / .35)" strokeWidth="2" fill="none"><path d="M110 30v14M50 44h120M50 44v12M110 44v12M170 44v12" /></g>
    <circle cx="110" cy="18" r="13" fill="var(--onb-accent)" />
    <path d="M103 14l3 3 4-5 4 5 3-3-2 9h-10z" fill="var(--onb-accent-ink)" />
    <g fill="oklch(0.30 0.05 290)" stroke="oklch(0.97 0.01 290 / .35)">
      <circle cx="50" cy="68" r="12" /><circle cx="110" cy="68" r="12" /><circle cx="170" cy="68" r="12" />
    </g>
    <g fill="oklch(0.88 0.10 160)"><circle cx="50" cy="68" r="4" /><circle cx="110" cy="68" r="4" /><circle cx="170" cy="68" r="4" /></g>
  </svg>
)

export default function Monomind({ profileId, reload, setCanContinue }) {
  const { t } = useTranslation()
  const run = useSyncExternalStore(subscribeMonomindInit, getMonomindInit)
  const [already, setAlready] = useState(false)
  const [name, setName] = useState(profileId)

  useEffect(() => {
    let live = true
    callStep(() => GoApp.IsMonomindInitialized()).then(r => { if (live && r.ok && r.value === true) setAlready(true) })
    callStep(() => GoApp.GetActiveProfile()).then(r => { if (live && r.ok && r.value?.name) setName(r.value.name) })
    return () => { live = false }
  }, [])

  // A run is shown while it goes (the Go side runs one at a time); its
  // result only counts for the profile it ran for.
  const mine = run.status === 'running' || run.profileId === profileId
  const on = already || (mine && run.status === 'done')
  const running = !on && run.status === 'running'
  const failed = mine && run.status === 'error'
  useEffect(() => { setCanContinue(on) }, [on, setCanContinue])

  let action
  if (on) {
    action = <p className="onb-mm-on" role="status"><Check /><span>{t('onboarding.monomind.on', { name })}</span></p>
  } else if (running) {
    action = (
      <div>
        <div className="onb-rows">
          <div className="onb-row busy" role="status">
            <span className="onb-dot"><span className="onb-spinner" /></span>
            <div className="onb-row-t">
              <b>{t('onboarding.monomind.running', { name })}</b>
              <span>{t('onboarding.monomind.runningHint')}</span>
            </div>
          </div>
        </div>
        {run.lines.length > 0 && (
          <ol className="onb-log" aria-label={t('onboarding.monomind.progress')}>
            {run.lines.map((l, i) => <li key={i}>{l}</li>)}
          </ol>
        )}
      </div>
    )
  } else {
    action = (
      <div>
        <p className="onb-muted onb-mm-note">{t('onboarding.monomind.agentNote')}</p>
        <button type="button" className="onb-btn primary" onClick={() => startMonomindInit(reload, profileId)}>
          {t('onboarding.monomind.turnOn', { name })}
        </button>
        {failed && (
          <p className="onb-error" role="alert">
            {run.loginRequired ? t('onboarding.shell.signIn') : t('onboarding.monomind.failed', { message: run.message })}
          </p>
        )}
      </div>
    )
  }

  return (
    <>
      <p className="onb-lead">{t('onboarding.monomind.lead')}</p>
      <div className="onb-grid2 onb-mm-cards">
        <div className="onb-card"><MiniDocs /><h2 className="onb-h2">{t('onboarding.monomind.docsTitle')}</h2><p>{t('onboarding.monomind.docsBody')}</p></div>
        <div className="onb-card"><MiniOrg /><h2 className="onb-h2">{t('onboarding.monomind.orgsTitle')}</h2><p>{t('onboarding.monomind.orgsBody')}</p></div>
      </div>
      {action}
      <p className="onb-pointer"><Pin /><span>{t('onboarding.monomind.pointer')}</span></p>
    </>
  )
}
