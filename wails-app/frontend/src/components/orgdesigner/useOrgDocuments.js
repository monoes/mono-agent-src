// Loads the Documents view (`org documents`) for one run and, for the live
// run, reloads it when the bus says a document changed. The stream carries no
// document state; the Go reader replays the store, so a reload is the truth.
import { useCallback, useEffect, useRef, useState } from 'react'
import { api, onOrgEvent } from '../../services/api.js'
import { isDocsEvent, normalizeView } from './orgDocuments.js'
import i18n from '../../i18n.js'

export const RELOAD_DEBOUNCE_MS = 250

export default function useOrgDocuments({ orgName, run = '', live = false }) {
  const [view, setView] = useState(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const seq = useRef(0)

  const load = useCallback(async (quiet) => {
    if (!orgName) return
    const mine = ++seq.current
    if (!quiet) setLoading(true)
    const res = await api.getOrgDocuments(orgName, run)
    if (mine !== seq.current) return // a newer load superseded this one
    const v = normalizeView(res)
    if (v) { setView(v); setError('') } else setError(res?.error || i18n.t('ui6OrgsDesigner.read_documents_failed'))
    setLoading(false)
  }, [orgName, run])

  useEffect(() => { setView(null); load(false); return () => { seq.current++ } }, [load])

  useEffect(() => {
    if (!live || !orgName) return undefined
    let timer = null
    const off = onOrgEvent((payload) => {
      if (payload?.orgName !== orgName || !isDocsEvent(payload.event)) return
      clearTimeout(timer)
      timer = setTimeout(() => load(true), RELOAD_DEBOUNCE_MS)
    })
    return () => { clearTimeout(timer); off() }
  }, [live, orgName, load])

  return { view, loading, error, reload: () => load(false) }
}
