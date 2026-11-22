// "Run setup again" (Settings > Application Info): clears the onboarding's
// completed mark and skips (RestartOnboarding; what was really done stays
// done), then the gate opens the onboarding at the step it reads next.
import { useContext, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { RotateCcw } from 'lucide-react'
import { OnboardingContext } from '../../onboarding/onboardingContext.js'
import { onboarding } from '../../services/onboarding.js'
import { callStep } from '../../onboarding/callStep.js'
import { markSetupStale } from '../../pages/dashboard/setupSession.js'

export default function RunSetupAgainRow() {
  const { t } = useTranslation()
  const { openOnboarding } = useContext(OnboardingContext)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const run = async () => {
    if (busy) return
    setBusy(true)
    setError('')
    const r = await callStep(() => onboarding.restart())
    if (!r.ok) {
      setError(r.loginRequired ? t('onboarding.shell.signIn') : r.message)
      setBusy(false)
      return
    }
    markSetupStale() // the dashboard card reads again once the onboarding closes
    await openOnboarding()
    setBusy(false)
  }

  return (
    <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 16 }}>
      <div style={{ minWidth: 0 }}>
        <span style={{ fontFamily: 'var(--font-mono)', fontSize: 10, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: 1 }}>
          {t('onboarding.runAgain.label')}
        </span>
        <div style={{ fontSize: 11, color: 'var(--text-muted)', marginTop: 2 }}>{t('onboarding.runAgain.hint')}</div>
        {error && <div role="alert" style={{ fontSize: 11, color: 'var(--red)', marginTop: 4 }}>{error}</div>}
      </div>
      <button className="btn btn-secondary btn-sm" style={{ gap: 5, flexShrink: 0 }} disabled={busy} onClick={run}>
        <RotateCcw size={12} /> {t('onboarding.runAgain.action')}
      </button>
    </div>
  )
}
