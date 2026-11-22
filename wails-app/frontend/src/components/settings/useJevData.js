import { useState, useEffect, useCallback } from 'react'
import { JevStatus, JevUsage, JevProviders } from '../../wailsjs/go/main/App'
import { errMsg } from './jevShared.jsx'

// The Jev status, the decision providers and (when a profile half is shown)
// the 7-day usage, read once for however many JevSection parts share them.
// Settings calls this once and hands the result to both parts as `jev`; a
// JevSection without that prop calls it itself. `load` reads them all again,
// after a save/test/remove/adopt or a provider change.
export function useJevData({ withUsage }) {
  const [status, setStatus] = useState(null)
  const [loadErr, setLoadErr] = useState('')
  const [usage, setUsage] = useState(null)
  const [usageErr, setUsageErr] = useState('')
  const [providers, setProviders] = useState(null)
  const [providersErr, setProvidersErr] = useState('')

  const load = useCallback(async () => {
    const [st, us, pr] = await Promise.allSettled([
      (async () => JevStatus())(),
      withUsage ? (async () => JevUsage('7d'))() : Promise.resolve(null),
      (async () => JevProviders())(),
    ])
    if (st.status === 'fulfilled') { setStatus(st.value); setLoadErr('') } else setLoadErr(errMsg(st.reason))
    if (pr.status === 'fulfilled') { setProviders(pr.value || null); setProvidersErr('') } else setProvidersErr(errMsg(pr.reason))
    if (!withUsage) return
    if (us.status === 'fulfilled') { setUsage(us.value); setUsageErr('') } else setUsageErr(errMsg(us.reason))
  }, [withUsage])

  useEffect(() => { load() }, [load])

  return { status, loadErr, usage, usageErr, providers, providersErr, load }
}
