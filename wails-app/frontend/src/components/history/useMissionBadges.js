import { useEffect, useState } from 'react'
import { sessionsApi } from '../../services/taskSessions.js'
import { sessionOf } from '../../lib/missionModel.js'

// useMissionBadges maps conversation id -> the state of its mission
// (draft, running, paused, done, ...), read while the drawer is open. A
// failed read shows no badges.
export function useMissionBadges(open, conversationIds) {
  const [sessions, setSessions] = useState([])
  useEffect(() => {
    if (!open) return undefined
    let live = true
    sessionsApi.list(true).then(doc => { if (live && !doc?.error) setSessions(Array.isArray(doc?.sessions) ? doc.sessions : []) })
    return () => { live = false }
  }, [open, conversationIds.length])
  const badges = {}
  for (const id of conversationIds) {
    const s = sessionOf(sessions, id)
    if (s) badges[id] = s.state
  }
  return badges
}
