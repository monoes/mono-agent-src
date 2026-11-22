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
// Over the running app: when the onboarding is opened for an existing install
// (openOnboarding from inside the app, or the short setup above) the app stays
// mounted underneath, inert and hidden from assistive tech, and keeps its
// state; the onboarding offers "Back to MonoAgent" (and Escape), and finishing
// navigates the mounted app (navRequest) instead of remounting it. On a first
// run (and the late switch, which is one) the app is not mounted, as before.
// After "Run setup again" (restart clears completed) a relaunch mid-way shows
// the onboarding first-run style; within the session the app stays underneath.
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import App from '../App.jsx'
import Onboarding from './Onboarding.jsx'
import { useOnboarding } from './useOnboarding.js'
import { OnboardingContext } from './onboardingContext.js'
import { pendingProfileSetup, clearProfileSetup } from './profileSetup.js'
import { markSetupStale } from '../pages/dashboard/setupSession.js'

// Pages inside the app import it from ./onboardingContext.js (no cycle).
export { OnboardingContext }

export const LATE_SWITCH_MS = 10000

export default function OnboardingGate({ banners = null }) {
  const { t } = useTranslation()
  const ob = useOnboarding()
  const { reload, loadFull } = ob
  // undefined: not decided yet; null: the app; { stepId, mode, over, n }: the
  // onboarding, over the mounted app when over is true
  const [open, setOpen] = useState(undefined)
  const [nav, setNav] = useState(null)
  const [navRequest, setNavRequest] = useState(null)
  const openCount = useRef(0)
  const openRef = useRef(open)
  openRef.current = open
  // what had focus in the app when the onboarding opened over it
  const appFocusRef = useRef(null)
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
    if (st && st.completed === false) first = { stepId: undefined, mode: 'full', over: false, n: 0 }
    else if (st && st.completed === true && forProfile && forProfile === st.profileId) first = { stepId: undefined, mode: 'profile', over: true, n: 0 }
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
      setOpen({ stepId: undefined, mode: 'full', over: false, n: ++openCount.current })
    } else if (!inTime || ob.state.completed) {
      lateRef.current = null
    }
  }, [open, ob.state])

  const openOnboarding = useCallback(async (stepId, mode = 'full') => {
    lateRef.current = null
    await loadFull()
    // opened from inside the app: the app is mounted, and stays so
    const prev = openRef.current
    if (prev === null) appFocusRef.current = document.activeElement
    setOpen({ stepId: typeof stepId === 'string' ? stepId : undefined, mode: mode === 'profile' ? 'profile' : 'full', over: prev === null || !!(prev && prev.over), n: ++openCount.current })
  }, [loadFull])

  const onFinish = useCallback((next) => {
    lateRef.current = null
    const to = next && next.page ? { page: next.page, opts: next.opts } : null
    // over the mounted app: navigate it; otherwise it mounts at the page
    const prev = openRef.current
    if (prev && prev.over) { if (to) setNavRequest({ ...to }) } else setNav(to)
    setOpen(null)
  }, [])

  // "Back to MonoAgent": closes the onboarding over the app, changing nothing.
  const backToApp = useCallback(() => { setOpen(null) }, [])

  // Closed: the dashboard's Finish setup card no longer has a standing answer
  // (it used to rely on the app remounting); from over the app, focus goes
  // back where it was in the app.
  const wasOverRef = useRef(false)
  const wasOpenRef = useRef(false)
  useEffect(() => {
    if (open === null && wasOpenRef.current) markSetupStale()
    if (open === null && wasOverRef.current) {
      const el = appFocusRef.current
      appFocusRef.current = null
      if (el && el.isConnected && el !== document.body && typeof el.focus === 'function') el.focus({ preventScroll: true })
    }
    wasOpenRef.current = !!open
    wasOverRef.current = !!(open && open.over)
  }, [open])

  const ctx = useMemo(() => ({ openOnboarding, state: ob.state, reload, loadFull }), [openOnboarding, ob.state, reload, loadFull])

  if (open === undefined) {
    return (
      <div role="status" style={{ width: '100vw', height: '100vh', display: 'flex', alignItems: 'center', justifyContent: 'center', background: 'var(--void)', fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-muted)' }}>
        {t('account.checking')}
      </div>
    )
  }
  const over = !!(open && open.over)
  return (
    <OnboardingContext.Provider value={ctx}>
      {(open === null || over) && (
        <div key="app" className="onb-app-host" style={{ display: 'contents' }} inert={over || undefined} aria-hidden={over || undefined} data-under-onboarding={over || undefined}>
          <App banners={banners} initialNav={nav} navRequest={navRequest} />
        </div>
      )}
      {open && (
        <Onboarding
          key={`${open.mode}:${open.stepId || ''}:${open.n}`}
          state={ob.state}
          startStep={open.stepId}
          mode={open.mode}
          onFinish={onFinish}
          onBack={over ? backToApp : undefined}
          over={over}
          reload={ob.reload}
          mark={ob.mark}
          skip={ob.skip}
          complete={ob.complete}
          banners={over ? null : banners}
        />
      )}
    </OnboardingContext.Provider>
  )
}
