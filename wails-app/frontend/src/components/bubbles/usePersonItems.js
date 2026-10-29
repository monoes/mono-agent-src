import { useCallback, useEffect, useRef, useState } from 'react'
import { personItemsApi } from '../../services/personItems.js'
import { usePageVisibleRef, useVisibleCatchUp } from '../../lib/usePageVisible.js'
import { planAlert, trimSeen } from '../../lib/personItems.js'
import { getNotifySound, playChime } from '../../lib/personChime.js'

export const PERSON_POLL_MS = 5000
const SEEN_KEY = 'monoagent:personItemsSeen'

function loadSeen() {
  try {
    const raw = JSON.parse(localStorage.getItem(SEEN_KEY) || '[]')
    return new Set(Array.isArray(raw) ? raw.filter(Number.isFinite) : [])
  } catch { return new Set() }
}

function saveSeen(seen) {
  try { localStorage.setItem(SEEN_KEY, JSON.stringify(trimSeen(seen))) } catch { /* best-effort */ }
}

// notifyPersonItems is the desktop notification for new items. It carries a count, never
// the plan's text (an agent wrote that).
export function notifyPersonItems(count, title, body) {
  if (typeof Notification === 'undefined' || Notification.permission !== 'granted') return
  try { new Notification(title, { body: body(count), tag: 'person-item' }) } catch { /* sandboxed webview */ }
}

// usePersonItems reads the items that wait for the person every few seconds while the window
// is visible, and says so when a new one appears: a chime (if the operator's notifySound is on,
// at most one per five seconds, never twice for one item) and a desktop notification.
// answer(verb, item, text) runs the CLI verb and reads the list again at once.
//   alerts { title, body(count) }: the notification's words, so the hook holds no copy.
export function usePersonItems({ enabled = true, alerts = null, play = playChime, now = Date.now } = {}) {
  const [items, setItems] = useState([])
  const [loaded, setLoaded] = useState(false)
  const visible = usePageVisibleRef()
  const seenRef = useRef(null)
  const lastChime = useRef(0)
  const epoch = useRef(0)
  const alertsRef = useRef(alerts)
  alertsRef.current = alerts

  const refresh = useCallback(async () => {
    const mine = ++epoch.current
    const res = await personItemsApi.list()
    if (mine !== epoch.current || res?.error || !Array.isArray(res?.items)) return
    if (seenRef.current == null) seenRef.current = loadSeen()
    const plan = planAlert({ items: res.items, seen: seenRef.current, lastChimeAt: lastChime.current, now: now(), soundOn: getNotifySound() })
    if (plan.fresh.length > 0) {
      seenRef.current = plan.seen
      saveSeen(plan.seen)
      if (plan.chime) {
        lastChime.current = plan.lastChimeAt
        try { play() } catch { /* a blocked or missing audio device is silence */ }
      }
      const a = alertsRef.current
      if (a) notifyPersonItems(plan.fresh.length, a.title, a.body)
    }
    setItems(res.items)
    setLoaded(true)
  }, [play, now])

  useEffect(() => {
    if (!enabled) return undefined
    refresh()
    const timer = setInterval(() => { if (visible.current) refresh() }, PERSON_POLL_MS)
    return () => { clearInterval(timer); epoch.current++ }
  }, [enabled, refresh, visible])
  useVisibleCatchUp(refresh)

  const answer = useCallback(async (verb, item, text = '') => {
    const id = item?.sessionId
    const res = verb === 'approve' ? await personItemsApi.approve(id, item?.rev ?? 0, item?.digest ?? '')
      : verb === 'deny' ? await personItemsApi.deny(id, text)
        : verb === 'ask' ? await personItemsApi.ask(id, text)
          : { error: 'unknown answer' }
    if (!res?.error || res.code === 'plan_changed') refresh()
    return res
  }, [refresh])

  return { items, loaded, refresh, answer }
}
