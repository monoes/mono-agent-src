// The onboarding's state, read from GetOnboardingState on mount and after every
// mark, skip, complete or restart. loading is true only until the first answer;
// later reloads keep the last good state on screen. error is the first load's
// failure message, '' otherwise. Every action resolves to callStep's result.
import { useCallback, useEffect, useRef, useState } from 'react'
import { onboarding } from '../services/onboarding.js'
import { callStep } from './callStep.js'

const readable = (v) => v && typeof v === 'object' && Array.isArray(v.steps)

export function useOnboarding() {
  const [state, setState] = useState(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const alive = useRef(true)
  const seq = useRef(0)

  useEffect(() => {
    alive.current = true
    return () => { alive.current = false }
  }, [])

  const reload = useCallback(async () => {
    const mine = ++seq.current
    let r = await callStep(() => onboarding.state())
    if (r.ok && !readable(r.value)) r = { ok: false, loginRequired: false, message: 'the onboarding state could not be read' }
    // an older answer that lands after a newer one is dropped
    if (!alive.current || mine !== seq.current) return r
    if (r.ok) {
      setState(r.value)
      setError('')
    } else {
      console.error('onboarding state:', r.message)
      setError(e => e || r.message)
    }
    setLoading(false)
    return r
  }, [])

  useEffect(() => { reload() }, [reload])

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

  return { state, loading, error, reload, mark, skip, complete, restart }
}
