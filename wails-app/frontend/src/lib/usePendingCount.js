// How many things wait for a person — workflow approvals, leads to review,
// drafts — from `summary --section hil`: local counts, no Jev call (unlike
// `hil list --suggest`, which asks Jev about every item it has not rated).
// Polls while the window is visible and raises a desktop notification when
// the count goes up. Org items are counted by the HIL drawer when it is open
// and by the dashboard (`org summary`).
import { useCallback, useEffect, useRef, useState } from 'react'
import { api } from '../services/api.js'
import { usePageVisibleRef, useVisibleCatchUp } from './usePageVisible.js'

export const PENDING_POLL_MS = 10000

export function pendingFromSummary(summary) {
  const h = summary?.hil
  if (!h || h.error) return null
  return (h.workflow_pending || 0) + (h.people_review || 0) + (h.drafts || 0)
}

export function notifyPending(added) {
  if (typeof Notification === 'undefined' || Notification.permission !== 'granted') return
  const body = added === 1 ? '1 new item is waiting for your review' : `${added} new items are waiting for your review`
  try { new Notification('Human in Loop', { body, tag: 'hil-pending' }) } catch { /* sandboxed webview */ }
}

// A coder session with no master that hit a risky gap waits for the person with no timeout
// (`summary` counts it in coder_items, per kind). The notification carries a count, never the question.
export function notifyCoderQuestions() {
  if (typeof Notification === 'undefined' || Notification.permission !== 'granted') return
  try { new Notification('Coder session', { body: 'A coder session is waiting for you (coder sessions questions)', tag: 'coder-question' }) } catch { /* sandboxed webview */ }
}

export function usePendingCount() {
  const [count, setCount] = useState(0)
  const last = useRef(null)
  const lastCoder = useRef(null)
  const visible = usePageVisibleRef()
  const load = useCallback(async () => {
    const summary = await api.getSummarySections('hil')
    const n = pendingFromSummary(summary)
    if (n == null) return // unknown: keep the last count
    const coder = Object.values(summary.hil.coder_items || {}).reduce((a, b) => a + b, 0)
    if (lastCoder.current != null && coder > lastCoder.current) notifyCoderQuestions()
    lastCoder.current = coder
    if (last.current != null && n > last.current) notifyPending(n - last.current)
    last.current = n
    setCount(n)
  }, [])
  useEffect(() => {
    load()
    const id = setInterval(() => { if (visible.current) load() }, PENDING_POLL_MS)
    return () => clearInterval(id)
    // visible is a ref: read at tick time, not a dependency.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [load])
  useVisibleCatchUp(load)
  return { count, reload: load }
}
