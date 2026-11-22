// Decides between the onboarding and the app, once the account allows work
// (AccountShell renders this as the shell). The first answer from
// GetOnboardingState decides: not completed shows the onboarding, completed the
// app exactly as before. Later answers never switch on their own; only
// finishing (onFinish) closes the onboarding and only openOnboarding opens it.
// One exception: when the first read missed its deadline (the app showed), a
// late answer saying "not completed" that lands within LATE_SWITCH_MS of the
// app showing still opens the onboarding, so a new install with a slow first
// start gets it; later than that the app stays, and the next launch shows it.
// While the first answer is pending it shows AccountShell's quiet checking
// view; if the state cannot be read it shows the app (never a broken gate).
// A completed install whose selected profile is the one NewProfileModal just
// created (profileSetup.js; its caller switched to it and reloaded) opens the
// short setup, mode 'profile', with the full state; the request is dropped
// after the first decision either way.
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import App from '../App.jsx'
import Onboarding from './Onboarding.jsx'
import { useOnboarding } from './useOnboarding.js'
import { OnboardingContext } from './onboardingContext.js'
import { pendingProfileSetup, clearProfileSetup } from './profileSetup.js'

// Pages inside the app import it from ./onboardingContext.js (no cycle).
export { OnboardingContext }

export const LATE_SWITCH_MS = 10000

export default function OnboardingGate({ banners = null }) {
  const { t } = useTranslation()
  const ob = useOnboarding()
  const { reload, loadFull } = ob
  // undefined: not decided yet; null: the app; { stepId, mode }: the onboarding
  const [open, setOpen] = useState(undefined)
  const [nav, setNav] = useState(null)
  // set when the app showed because the first read missed its deadline
  const lateRef = useRef(null)
  const settledRef = useRef(false)

  if (open === undefined && !ob.loading) {
    // only an answer that says "not completed" opens it; a completed one with
    // a short-setup request for its selected profile opens that; anything
    // else is the app
    const st = ob.state
    const forProfile = pendingProfileSetup()
    let first = null
    if (st && st.completed === false) first = { stepId: undefined, mode: 'full' }
    else if (st && st.completed === true && forProfile && forProfile === st.profileId) first = { stepId: undefined, mode: 'profile' }
    lateRef.current = first === null && ob.timedOut ? Date.now() : null
    setOpen(first)
  }

  // Once decided: drop the short-setup request, and give the short setup the
  // full state (the fast answer's monomind and browser statuses are marks only).
  useEffect(() => {
    if (open === undefined || settledRef.current) return
    settledRef.current = true
    clearProfileSetup()
    if (open && open.mode === 'profile') loadFull()
  }, [open, loadFull])

  useEffect(() => {
    if (open !== null || lateRef.current === null || !ob.state) return
    const inTime = Date.now() - lateRef.current < LATE_SWITCH_MS
    if (ob.state.completed === false && inTime) {
      lateRef.current = null
      setOpen({ stepId: undefined, mode: 'full' })
    } else if (!inTime || ob.state.completed) {
      lateRef.current = null
    }
  }, [open, ob.state])

  const openOnboarding = useCallback(async (stepId, mode = 'full') => {
    lateRef.current = null
    await loadFull()
    setNav(null)
    setOpen({ stepId: typeof stepId === 'string' ? stepId : undefined, mode: mode === 'profile' ? 'profile' : 'full' })
  }, [loadFull])

  const onFinish = useCallback((next) => {
    lateRef.current = null
    setNav(next && next.page ? { page: next.page, opts: next.opts } : null)
    setOpen(null)
  }, [])

  const ctx = useMemo(() => ({ openOnboarding, state: ob.state, reload, loadFull }), [openOnboarding, ob.state, reload, loadFull])

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
