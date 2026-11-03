// Polls the control room while the page is the active one and the window is visible. One request chain, never
// overlapping; MIN_POLL_MS between polls, doubling after failures (nextDelay). Nothing runs when hidden or inactive.
import { useCallback, useEffect, useRef, useState } from 'react'
import { controlRoomApi } from '../../services/controlRoom.js'
import { usePageVisible } from '../../lib/usePageVisible.js'
import { MIN_POLL_MS, viewOf, nextDelay, mergeFeed, addSample, approxRate, num, FEED_PAGE, FEED_KEEP } from './controlRoomModel.js'

export function useControlRoom(active, api = controlRoomApi) {
  const visible = usePageVisible()
  const [doc, setDoc] = useState(null)
  const [feed, setFeed] = useState({ items: [], next: 0, older: false })
  const [rate, setRate] = useState(null)
  const [updatedAt, setUpdatedAt] = useState(0)
  const samples = useRef([])
  const failures = useRef(0)
  const timer = useRef(null)
  const lastStart = useRef(0)
  const alive = useRef(true)
  const olderLoaded = useRef(false)

  const poll = useCallback(async () => {
    lastStart.current = Date.now()
    const ov = await api.overview()
    if (!alive.current) return 'stop'
    const v = viewOf(ov)
    failures.current = v.state === 'error' ? failures.current + 1 : 0
    setDoc(ov)
    setUpdatedAt(Date.now())
    if (v.state === 'ok' && v.chain === 'yes' && ov.spend) {
      samples.current = addSample(samples.current, { t: Date.now(), used: num(ov.spend.used_micro), run: ov.run?.run_id })
      setRate(approxRate(samples.current))
    } else {
      samples.current = []
      setRate(null)
    }
    if (v.state === 'ok' && v.chain === 'yes') {
      const page = await api.feed(0, FEED_PAGE)
      if (!alive.current) return 'stop'
      if (!page?.error && Array.isArray(page?.items)) {
        setFeed(f => ({ items: mergeFeed(f.items, page.items), next: olderLoaded.current ? f.next : num(page.next_before), older: olderLoaded.current }))
      }
    } else {
      olderLoaded.current = false
      setFeed({ items: [], next: 0, older: false })
    }
    return v.state
  }, [api])

  useEffect(() => {
    alive.current = true
    if (!active || !visible) return undefined
    let cancelled = false
    const loop = async () => {
      const state = await poll()
      if (cancelled || state === 'stop') return
      const wait = Math.max(nextDelay(failures.current, state), lastStart.current + MIN_POLL_MS - Date.now())
      timer.current = setTimeout(loop, wait)
    }
    loop()
    return () => { cancelled = true; clearTimeout(timer.current) }
  }, [active, visible, poll])

  useEffect(() => () => { alive.current = false }, [])

  const loadOlder = useCallback(async () => {
    const before = feed.next
    if (!before || feed.items.length >= FEED_KEEP) return
    const page = await api.feed(before, FEED_PAGE)
    if (!alive.current || page?.error || !Array.isArray(page?.items)) return
    olderLoaded.current = true
    setFeed(f => ({ items: mergeFeed(f.items, page.items), next: num(page.next_before), older: true }))
  }, [api, feed.next, feed.items.length])

  return { doc, view: viewOf(doc), feed, rate, updatedAt, loadOlder, refresh: poll }
}
