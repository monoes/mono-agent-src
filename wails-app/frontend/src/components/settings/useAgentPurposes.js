import { useState, useEffect, useCallback, useRef } from 'react'
import { api } from '../../services/api.js'
import { parsePurposes, setArgs } from '../../lib/agentPurposes.js'

// State of the Agent purposes pane: the CLI's document, plus a pending flag
// and an inline error per purpose. A CLI refusal ({error, code}) is kept with
// its code so the card can tell "not allowed" from "try again".
export function useAgentPurposes() {
  const [doc, setDoc] = useState(null)
  const [loadErr, setLoadErr] = useState('')
  const [pending, setPending] = useState({})
  const [errors, setErrors] = useState({})
  const alive = useRef(true)
  useEffect(() => () => { alive.current = false }, [])

  const load = useCallback(async () => {
    let res
    try { res = await api.agentPurposes() } catch (e) { res = { error: String(e?.message || e) } }
    if (!alive.current) return false
    if (res?.error) { setLoadErr(res.error); return false }
    try { setDoc(parsePurposes(res)); setLoadErr(''); return true } catch (e) { setLoadErr(String(e?.message || e)); return false }
  }, [])
  useEffect(() => { load() }, [load])

  const run = useCallback(async (id, call) => {
    setPending(p => ({ ...p, [id]: true }))
    setErrors(e => ({ ...e, [id]: null }))
    const res = await call()
    if (!alive.current) return false
    if (res?.error) {
      setErrors(e => ({ ...e, [id]: { message: String(res.error), code: res.code || '' } }))
      setPending(p => ({ ...p, [id]: false }))
      return false
    }
    await load()
    if (alive.current) setPending(p => ({ ...p, [id]: false }))
    return true
  }, [load])

  const save = useCallback((id, draft) => run(id, () => api.agentPurposeSet(...setArgs(id, draft))), [run])
  const reset = useCallback((id) => run(id, () => api.agentPurposeReset(id)), [run])
  const clearError = useCallback((id) => setErrors(e => ({ ...e, [id]: null })), [])

  return { doc, loadErr, load, pending, errors, save, reset, clearError }
}
