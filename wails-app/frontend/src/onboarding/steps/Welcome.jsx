// Step 1, Welcome: what MonoAgent is and what this setup will cover.
// "Get started" writes the welcome mark; nothing else happens here.
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import './Welcome.css'

const ICONS = {
  user: <><circle cx="12" cy="8" r="4" /><path d="M4 21c1-4.4 4.2-6.5 8-6.5s7 2.1 8 6.5" /></>,
  engine: <><circle cx="12" cy="12" r="3.2" /><path d="M12 2.5v3M12 18.5v3M2.5 12h3M18.5 12h3M5.3 5.3l2.1 2.1M16.6 16.6l2.1 2.1M5.3 18.7l2.1-2.1M16.6 7.4l2.1-2.1" /></>,
  bot: <><rect x="5" y="8" width="14" height="11" rx="3" /><path d="M12 4v4M9.5 13h.01M14.5 13h.01" /></>,
  globe: <><circle cx="12" cy="12" r="9" /><path d="M3 12h18M12 3c2.6 2.6 3.8 5.6 3.8 9s-1.2 6.4-3.8 9c-2.6-2.6-3.8-5.6-3.8-9S9.4 5.6 12 3z" /></>,
  play: <><circle cx="12" cy="12" r="9" /><path d="M10 8.5l5.5 3.5-5.5 3.5z" /></>,
}
const Icon = ({ name }) => (
  <svg viewBox="0 0 24 24" aria-hidden="true" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
    {ICONS[name]}
  </svg>
)

export default function Welcome({ mark, setCanContinue, setOnContinue, setContinueLabel }) {
  const { t } = useTranslation()
  const [error, setError] = useState('')

  useEffect(() => {
    setContinueLabel(t('onboarding.welcome.start'))
    setCanContinue(true)
  }, [t, setCanContinue, setContinueLabel])

  useEffect(() => {
    setOnContinue(async () => {
      setError('')
      const r = await mark('welcome')
      if (r && r.ok === false) {
        setError(r.loginRequired ? t('onboarding.shell.signIn') : r.message)
        return false
      }
      return true
    })
  })

  return (
    <div className="onb-welcome">
      <p className="onb-lead">{t('onboarding.welcome.lead')}</p>
      <p className="onb-h3">{t('onboarding.welcome.listTitle')}</p>
      <ul className="onb-welcome-list">
        <li><Icon name="user" />{t('onboarding.welcome.items.profile')}</li>
        <li><Icon name="engine" />{t('onboarding.welcome.items.engine')}</li>
        <li><Icon name="bot" />{t('onboarding.welcome.items.agent')}</li>
        <li><Icon name="globe" />{t('onboarding.welcome.items.browser')}</li>
        <li className="wide"><Icon name="play" />{t('onboarding.welcome.items.firstRun')}</li>
      </ul>
      {error && <p className="onb-error" role="alert">{error}</p>}
    </div>
  )
}
