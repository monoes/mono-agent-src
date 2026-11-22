import { useCallback, useEffect, useRef, useState } from 'react'
import { api, onMonomindInitEvent } from '../services/api.js'
import { useReloadOnActivate } from './useReloadOnActivate'

// useMonomindNotInitialized: whether the selected profile's folder still
// needs `monomind init` (IsMonomindInitialized, a file check). It is read on
// mount, again on every monomind:initProgress "done" (whichever page or the
// onboarding started that run, so a kept-mounted page does not keep a stale
// prompt), and again each time the page becomes active (a switch of profile
// made while it was hidden). Only the latest answer is applied.
// Returns [notInitialized, recheck].
export function useMonomindNotInitialized(isActive = true) {
  const [notInitialized, setNotInitialized] = useState(false)
  const seq = useRef(0)
  const recheck = useCallback(() => {
    const mine = ++seq.current
    return api.isMonomindInitialized().then(v => { if (mine === seq.current) setNotInitialized(!v) })
  }, [])
  useEffect(() => { recheck() }, [recheck])
  useEffect(() => onMonomindInitEvent((payload) => { if (payload?.kind === 'done') recheck() }), [recheck])
  useReloadOnActivate(isActive, recheck)
  return [notInitialized, recheck]
}
