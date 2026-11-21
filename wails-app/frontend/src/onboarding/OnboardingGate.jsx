// Decides between the onboarding and the app, once the account allows work
// (AccountShell renders this as the shell). The first answer from
// GetOnboardingState decides: not completed shows the onboarding, completed the
// app exactly as before. Later answers never switch on their own; only
// finishing (onFinish) closes the onboarding and only openOnboarding opens it.
// While the first answer is pending it shows AccountShell's quiet checking
// view; if the state cannot be read it shows the app (never a broken gate).
import { useCallback, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import App from '../App.jsx'
import Onboarding from './Onboarding.jsx'
import { useOnboarding } from './useOnboarding.js'
import { OnboardingContext } from './onboardingContext.js'

// Pages inside the app import it from ./onboardingContext.js (no cycle).
export { OnboardingContext }

export default function OnboardingGate({ banners = null }) {
  const { t } = useTranslation()
  const ob = useOnboarding()
  const { reload } = ob
  // undefined: not decided yet; null: the app; { stepId, mode }: the onboarding
  const [open, setOpen] = useState(undefined)
  const [nav, setNav] = useState(null)

  if (open === undefined && !ob.loading) {
    // only an answer that says "not completed" opens it; anything else is the app
    setOpen(ob.state && ob.state.completed === false ? { stepId: undefined, mode: 'full' } : null)
  }

  const openOnboarding = useCallback(async (stepId, mode = 'full') => {
    await reload()
    setNav(null)
    setOpen({ stepId: typeof stepId === 'string' ? stepId : undefined, mode: mode === 'profile' ? 'profile' : 'full' })
  }, [reload])

  const onFinish = useCallback((next) => {
    setNav(next && next.page ? { page: next.page, opts: next.opts } : null)
    setOpen(null)
  }, [])

  const ctx = useMemo(() => ({ openOnboarding, state: ob.state, reload }), [openOnboarding, ob.state, reload])

  if (open === undefined) {
    return (
      <div role="status" style={{ width: '100vw', height: '100vh', display: 'flex', alignItems: 'center', justifyContent: 'center', background: 'var(--void)', fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-muted)' }}>
        {t('account.checking')}
      </div>
    )
  }
  return (
    <OnboardingContext.Provider value={ctx}>
      {open
        ? (
          <Onboarding
            key={`${open.mode}:${open.stepId || ''}`}
            state={ob.state}
            startStep={open.stepId}
            mode={open.mode}
            onFinish={onFinish}
            reload={ob.reload}
            mark={ob.mark}
            skip={ob.skip}
            complete={ob.complete}
            banners={banners}
          />
        )
        : <App banners={banners} initialNav={nav} />}
    </OnboardingContext.Provider>
  )
}
