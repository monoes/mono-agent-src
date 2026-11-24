import { useState, useEffect, useCallback, useRef } from 'react'
import { api } from '../../services/api.js'
import { itemsOf } from './orgsPanelParts.jsx'

// The org rail's list + first-load sequencing, lifted out of OrgsPanel.
export default function useOrgList(effectiveOpen) {
  const [orgs, setOrgs] = useState([])
  const [loadingOrgs, setLoadingOrgs] = useState(true)
  const [notInitialized, setNotInitialized] = useState(false)

  const loadOrgs = useCallback(async (silent = false) => {
    if (!silent) setLoadingOrgs(true)
    try {
      // Checked first: ListOrgDesigns silently succeeds with an empty list
      // on a never-initialized profile (EnsureLayout creates an empty
      // .monomind/ on every org call), so it can't distinguish "no orgs
      // yet" from "monomind was never set up here" on its own.
      const initialized = await api.isMonomindInitialized()
      setNotInitialized(!initialized)
      if (!initialized) { setOrgs([]); return 0 }
      // listOrgDesigns reads config files directly (wails-app/app_orgs_design.go)
      // and so includes every org, including ones designed but never run —
      // listOrgs (below) proxies `monoagentcli org list`'s RUNTIME view,
      // which only knows about orgs that have actually been started at
      // least once. The rail needs the former or a freshly-designed org is
      // invisible until its first run.
      const res = await api.listOrgDesigns()
      const items = itemsOf(res)
      setOrgs(items)
      return items.length
    } finally {
      if (!silent) setLoadingOrgs(false)
    }
  }, [])

  // First activation loads with the spinner; every later re-activation
  // (coming back to the Orgs page / reopening an embedded panel) silently
  // refreshes the org list so it doesn't drift while hidden.
  const firstLoadRef = useRef(true)
  useEffect(() => {
    if (!effectiveOpen) return
    const isFirst = firstLoadRef.current
    if (!isFirst) {
      loadOrgs(true)
      return
    }
    firstLoadRef.current = false
    // The very first load of the app's session can race Go's own startup
    // (the selected profile is only known once App.startup() has resolved
    // it — Wails doesn't guarantee the frontend waits for that) — landing on
    // Orgs before it resolves queries no profile, so the panel shows an
    // error or the "not set up" prompt until the user navigates away and
    // back. Poll App.IsReady() (cheap, in-memory,
    // set as the very last line of startup()) instead of guessing with a
    // fixed delay — a cold start with the social build's node-registry
    // scan can take longer than a one-shot timer accounts for.
    let cancelled = false
    let started = false
    ;(async () => {
      for (let i = 0; i < 30 && !cancelled; i++) { // ~6s ceiling — startup finishing this slow would be a real problem elsewhere too
        if (await api.isReady()) break
        await new Promise(r => setTimeout(r, 200))
      }
      if (cancelled) return
      // silent:false (the real bug this whole effect was chasing) — the
      // prior version called loadOrgs(isFirst), which for the very first
      // activation passes isFirst=true straight through as the silent
      // argument. silent=true skips BOTH setLoadingOrgs(true) (harmless,
      // already true from useState's initial value) AND, critically,
      // setLoadingOrgs(false) in loadOrgs' own finally block — so the
      // spinner that's already showing from mount never clears. The panel
      // then sits on its loading state forever until some LATER activation
      // (this effect's isFirst=false branch above) finally passes
      // silent=false for the first time and clears it — exactly "first
      // click shows nothing, navigate away and back and it loads".
      started = true
      loadOrgs(false)
    })()
    return () => {
      cancelled = true
      // React StrictMode (development only) double-invokes every effect on
      // initial mount: run it, run its cleanup, run it again. firstLoadRef
      // is a ref, so it survives that cleanup — meaning the FIRST
      // invocation's `firstLoadRef.current = false` above was still visible
      // to the SECOND invocation, which then wrongly took the `!isFirst`
      // silent branch and never called loadOrgs(false) at all. The result:
      // orgs loads correctly in the background, but the spinner (already
      // true from useState's initial value) never clears — permanently,
      // since firstLoadRef had already been consumed. Un-claiming
      // firstLoadRef here, but ONLY when this invocation's own polling
      // never actually reached loadOrgs(false) (`started` is still false),
      // fixes that: the surviving invocation correctly sees isFirst=true
      // again and runs the real sequence. A genuine completed first load
      // (started=true) leaves firstLoadRef consumed as before, so later
      // re-activations still correctly take the fast silent-refresh path.
      if (!started) firstLoadRef.current = true
    }
  }, [effectiveOpen, loadOrgs])

  return { orgs, loadingOrgs, notInitialized, loadOrgs }
}
