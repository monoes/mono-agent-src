import { useState, useEffect } from 'react'
import { api } from '../services/api.js'

const QUIPS = [
  'Waking up the agents…',
  'Teaching the robots to knock before entering…',
  'Counting your profiles. Twice.',
  'Untangling the workflows…',
  'Convincing the database to get out of bed…',
  'Polishing the vault. Not peeking inside.',
  'Herding the browser tabs…',
  'Asking the nodes to line up nicely…',
]

const POLL_MS = 200
const GIVE_UP_MS = 30000 // never trap the user behind the overlay
const QUIP_MS = 1800

// Blocks the window until the backend has finished startup (migrations,
// profile selection); profile-scoped reads fail before that.
export default function StartupOverlay() {
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
        <div className="startup-overlay__quip">{QUIPS[quip]}</div>
      </div>
    </div>
  )
}
