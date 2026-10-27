import { useCallback, useEffect, useState } from 'react'
import { api } from '../../services/api.js'
import { cachedAgentScan, isMonomindNotFound } from '../../lib/agentRuntimes.js'
import { chosenAccess, isCoderAccess } from '../../lib/chatAccess.js'
import { coderReady, coderRuntimes } from '../chat/useCoderMode.js'

// useChatSetup is a new chat's choices before its first message: the access
// level, the runtime, the model and the effort. What is offered follows the
// level: a scoped chat runs on any installed runtime (the scan the assistant
// panel used), a coding level only on the runtimes `coder status` reports
// ready. It writes the resolved defaults back through onChange, so what the
// view shows is what create() sends.
//
// `state` says why a chat cannot be sent yet: loading | scan-missing |
// scan-error | no-runtime | no-models | ready.
export function useChatSetup(active, setup, onChange) {
  const [status, setStatus] = useState(undefined) // undefined: asking; null: could not be read
  const [scan, setScan] = useState({ done: false, error: '', missing: false, agents: [] })
  const [models, setModels] = useState(null) // null: loading

  useEffect(() => {
    if (!active) return undefined
    let live = true
    Promise.resolve().then(() => api.coderStatus()).then(s => { if (live) setStatus(s || null) }).catch(() => { if (live) setStatus(null) })
    return () => { live = false }
  }, [active])

  const settled = status !== undefined
  const coderOk = !!status?.enabled && coderReady(status)
  const access = settled ? chosenAccess(setup.access, status, coderOk) : (setup.access || 'scoped')
  const coder = isCoderAccess(access)

  // Scoped chats run on any installed runtime; the scan is slow, so it runs
  // only when the draft is scoped, and its result is cached app-wide.
  const [scanTry, setScanTry] = useState(0)
  useEffect(() => {
    if (!active || !settled || coder) return undefined
    let live = true
    setScan(s => ({ ...s, done: false, error: '', missing: false }))
    Promise.resolve().then(() => cachedAgentScan()).then(res => {
      if (!live) return
      if (!res || res.error) {
        const msg = res?.error || ''
        setScan({ done: true, agents: [], missing: isMonomindNotFound(msg), error: isMonomindNotFound(msg) ? '' : (msg || 'the agent runtime scan failed') })
        return
      }
      setScan({ done: true, error: '', missing: false, agents: (res.agents || []).filter(a => a.installed) })
    }).catch(err => { if (live) setScan({ done: true, agents: [], missing: false, error: String(err?.message || err) }) })
    return () => { live = false }
  }, [active, settled, coder, scanTry])

  const options = coder ? coderRuntimes(status).filter(r => r.ready) : scan.agents
  const optionKey = options.map(r => r.id).join(',')
  const runtime = options.some(r => r.id === setup.runtime) ? setup.runtime : ''
  const info = coder ? options.find(r => r.id === runtime) || null : null
  const binary = coder ? '' : options.find(r => r.id === runtime)?.binary || ''

  // Write the resolved level back, and swap a runtime the level does not offer
  // for the first it does.
  useEffect(() => {
    if (!active || !settled) return
    // Applied only if the draft's level is still the one this render saw: this effect runs
    // after the commit, and a level the user picked in between must not be put back.
    if (setup.access !== access) onChange(cur => (cur.access === setup.access ? { access } : {}))
    if (options.length > 0 && !options.some(r => r.id === setup.runtime)) onChange(cur => (cur.runtime === setup.runtime ? { runtime: options[0].id, model: '', effort: '' } : {}))
    // options is rebuilt every render; optionKey is its identity.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [active, settled, access, optionKey, setup.runtime, setup.access, onChange])

  useEffect(() => {
    if (!active || !runtime) { setModels(null); return undefined }
    let live = true
    setModels(null)
    Promise.resolve().then(() => api.getAgentRuntimeModels(runtime, binary)).then(list => {
      if (!live) return
      const items = Array.isArray(list) ? list : []
      setModels(items)
      if (items.length && !items.some(m => m.id === setup.model)) onChange({ model: items[0].id, effort: '' })
    }).catch(() => { if (live) setModels([]) })
    return () => { live = false }
    // Loaded once per runtime; setup.model only seeds the default.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [active, runtime, binary])

  const model = (models || []).find(m => m.id === setup.model)
  const efforts = (!coder || info?.effort !== false) && Array.isArray(model?.effort_levels) ? model.effort_levels : []

  let state = 'ready'
  if (!settled || (!coder && !scan.done)) state = 'loading'
  else if (!coder && scan.missing) state = 'scan-missing'
  else if (!coder && scan.error) state = 'scan-error'
  else if (options.length === 0) state = 'no-runtime'
  else if (models !== null && models.length === 0) state = 'no-models'
  else if (!runtime || models === null || !setup.model) state = 'loading'

  const retryScan = useCallback(() => setScanTry(n => n + 1), [])
  return { status, settled, coderOk, access, coder, options, runtime, info, models, efforts, state, scanError: scan.error, retryScan }
}
