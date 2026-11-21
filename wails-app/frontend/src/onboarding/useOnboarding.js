// The onboarding's state, read on mount and after every mark, skip, complete
// or restart. loading is true only until the first answer; later reloads keep
// the last good state on screen. error is the first load's failure message, ''
// otherwise. Every action resolves to callStep's result.
// Two reads: GetOnboardingState is fast (stored marks and the profile count
// only; its other statuses are marks, not facts), so the gate decides from it.
// GetOnboardingStateFull reads every fact. loadFull() switches the hook to the
// full read for good and reads with it; from then on reload() and every
// action's re-read use it too. The gate calls it when it opens the onboarding
// over a finished state; a fast answer saying "not completed" is painted at
// once and followed by a full read by itself.
// timedOut is true when the first read missed FIRST_READ_DEADLINE_MS.
import { useCallback, useEffect, useRef, useState } from 'react'
import { onboarding } from '../services/onboarding.js'
import { callStep } from './callStep.js'

const readable = (v) => v && typeof v === 'object' && Array.isArray(v.steps)
const READY_POLL_MS = 200
const READY_GIVE_UP_MS = 30000 // StartupOverlay's limit: never trap the user
export const FIRST_READ_DEADLINE_MS = 3000 // an existing user never waits longer on "Checking…"

export function useOnboarding() {
  const [state, setState] = useState(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const alive = useRef(true)
  const seq = useRef(0)
  const full = useRef(false)
  const [timedOut, setTimedOut] = useState(false)
  const reloadRef = useRef(null)

  useEffect(() => {
    alive.current = true
    return () => { alive.current = false }
  }, [])

  const reload = useCallback(async () => {
    const mine = ++seq.current
    let r = await callStep(() => (full.current ? onboarding.stateFull() : onboarding.state()))
    if (r.ok && !readable(r.value)) r = { ok: false, loginRequired: false, message: 'the onboarding state could not be read' }
    // an older answer that lands after a newer one is dropped
    if (!alive.current || mine !== seq.current) return r
    if (r.ok) {
      // an install that is not finished shows the onboarding: full reads from now on
      const switching = r.value.completed === false && !full.current
      if (switching) full.current = true
      setState(r.value)
      if (switching) reloadRef.current()
      setError('')
    } else {
      console.error('onboarding state:', r.message)
      setError(e => e || r.message)
    }
    setLoading(false)
    return r
  }, [])
  reloadRef.current = reload

  // The first read waits for the backend's startup (migrations, profile
  // selection), as StartupOverlay does: before it, the profile count cannot be
  // read and an existing install would look unfinished. A missing IsReady
  // binding counts as ready; after READY_GIVE_UP_MS it reads anyway.
  // The read itself has FIRST_READ_DEADLINE_MS: past it, loading ends with an
  // error (the gate shows the app) and the late answer only updates state.
  useEffect(() => {
    let live = true
    let deadline
    ;(async () => {
      const started = Date.now()
      while (live && Date.now() - started < READY_GIVE_UP_MS) {
        const r = await callStep(() => onboarding.ready())
        if (!r.ok || r.value === true) break
        await new Promise(res => setTimeout(res, READY_POLL_MS))
      }
      if (!live) return
      deadline = setTimeout(() => {
        if (!alive.current) return
        console.error(`onboarding state: no answer within ${FIRST_READ_DEADLINE_MS} ms`)
        setError(e => e || 'the onboarding state took too long')
        setTimedOut(true)
        setLoading(false)
      }, FIRST_READ_DEADLINE_MS)
      await reload()
      clearTimeout(deadline)
    })()
    return () => { live = false; clearTimeout(deadline) }
  }, [reload])

  const loadFull = useCallback(() => {
    full.current = true
    return reload()
  }, [reload])

  // write: one binding call, then a fresh read when it worked; the call's result.
  const write = useCallback(async (call) => {
    const r = await callStep(call)
    if (r.ok) await reload()
    return r
  }, [reload])

  const mark = useCallback((id) => write(() => onboarding.mark(id)), [write])
  const skip = useCallback((id) => write(() => onboarding.skip(id)), [write])
  const complete = useCallback(() => write(() => onboarding.complete()), [write])
  const restart = useCallback(() => write(() => onboarding.restart()), [write])

  return { state, loading, error, timedOut, reload, loadFull, mark, skip, complete, restart }
}
