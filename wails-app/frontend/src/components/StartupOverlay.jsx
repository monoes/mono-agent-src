import { useState, useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import { api } from '../services/api.js'

const QUIPS = [
  'ui6Set.quip_agents',
  'ui6Set.quip_knock',
  'ui6Set.quip_profiles',
  'ui6Set.quip_workflows',
  'ui6Set.quip_database',
  'ui6Set.quip_vault',
  'ui6Set.quip_tabs',
  'ui6Set.quip_nodes',
]

const POLL_MS = 200
const GIVE_UP_MS = 30000 // never trap the user behind the overlay
const QUIP_MS = 1800

// Blocks the window until the backend has finished startup (migrations,
// profile selection); profile-scoped reads fail before that.
export default function StartupOverlay() {
  const { t } = useTranslation()
  const [ready, setReady] = useState(false)
  const [quip, setQuip] = useState(() => Math.floor(Math.random() * QUIPS.length))

  useEffect(() => {
    let cancelled = false
    const started = Date.now()
    ;(async () => {
      while (!cancelled && Date.now() - started < GIVE_UP_MS) {
        if (await api.isReady()) break
        await new Promise(r => setTimeout(r, POLL_MS))
      }
      if (!cancelled) setReady(true)
    })()
    return () => { cancelled = true }
  }, [])

  useEffect(() => {
    if (ready) return undefined
    const id = setInterval(() => setQuip(q => (q + 1) % QUIPS.length), QUIP_MS)
    return () => clearInterval(id)
  }, [ready])

  if (ready) return null
  return (
    <div className="startup-overlay" role="status" aria-live="polite" aria-busy="true">
      <div className="startup-overlay__box">
        <div className="startup-overlay__title">Mono Agent</div>
        <div className="startup-overlay__bar"><div className="startup-overlay__fill" /></div>
        <div className="startup-overlay__quip">{t(QUIPS[quip])}</div>
      </div>
    </div>
  )
}
