// Loads everything the designer needs to show automations next to the org
// chart: org automations, grants, autonomy (for tier previews), and whether
// the workflow engine daemon is up (automation roles need it). All reads go
// through `monoagentcli` bindings; `refresh()` re-reads after a mutation.
import { useCallback, useEffect, useRef, useState } from 'react'
import { api } from '../../services/api.js'
import i18n from '../../i18n.js'

export const DAEMON_POLL_MS = 30_000

function arr(res, key) {
  return res && !res.error && Array.isArray(res[key]) ? res[key] : []
}

/** true when running, false when the CLI says it is not, null when unknown. */
export function daemonRunning(status) {
  if (!status || status.error || !status.daemon) return null
  return status.daemon.running === true
}

export default function useOrgAutomations(orgName) {
  const [automations, setAutomations] = useState([])
  const [grants, setGrants] = useState([])
  const [autonomy, setAutonomy] = useState(null)
  const [daemon, setDaemon] = useState(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const [version, setVersion] = useState(0)
  const orgRef = useRef(orgName)
  orgRef.current = orgName

  const refresh = useCallback(async () => {
    if (!orgName) return
    const [autos, grantRes, auto] = await Promise.all([
      api.listOrgAutomations(orgName),
      api.listOrgGrants(orgName),
      api.getOrgAutonomy(orgName),
    ])
    if (orgRef.current !== orgName) return
    setAutomations(arr(autos, 'automations'))
    setGrants(arr(grantRes, 'grants'))
    setAutonomy(auto && !auto.error ? auto : null)
    const err = autos?.error || grantRes?.error || (autos == null ? i18n.t('ui6OrgsDesigner.load_automations_failed') : '')
    setError(err || '')
    setLoading(false)
    setVersion(v => v + 1)
  }, [orgName])

  useEffect(() => {
    setLoading(true)
    setAutomations([])
    setGrants([])
    refresh()
  }, [refresh])

  useEffect(() => {
    let cancelled = false
    const poll = async () => {
      const res = await api.getDaemonStatus()
      if (!cancelled) setDaemon(daemonRunning(res))
    }
    poll()
    const iv = setInterval(poll, DAEMON_POLL_MS)
    return () => { cancelled = true; clearInterval(iv) }
  }, [])

  return { automations, grants, autonomy, daemonRunning: daemon, error, loading, refresh, version }
}
