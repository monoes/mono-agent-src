// Time helpers for waiting items (C-41): how long an approval or gate has
// waited, and how long until the org idle-stops.
import i18nOr from '../../lib/i18nOr.js'

/** Epoch ms from a number (ms, or seconds when small) or an ISO string; null if unknown. */
export function toMillis(ts) {
  if (ts == null || ts === '') return null
  if (typeof ts === 'number') return ts < 1e12 ? ts * 1000 : ts
  const n = Number(ts)
  if (!Number.isNaN(n) && String(ts).trim() !== '') return n < 1e12 ? n * 1000 : n
  const t = Date.parse(ts)
  return Number.isNaN(t) ? null : t
}

/** Compact duration: 45s, 3m, 3m 20s (under 10 min), 1h 20m, 2d 3h. */
export function formatDuration(ms) {
  if (ms == null || Number.isNaN(ms)) return ''
  const s = Math.max(0, Math.floor(ms / 1000))
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 10) return s % 60 ? `${m}m ${s % 60}s` : `${m}m`
  if (m < 60) return `${m}m`
  const h = Math.floor(m / 60)
  if (h < 24) return m % 60 ? `${h}h ${m % 60}m` : `${h}h`
  const d = Math.floor(h / 24)
  return h % 24 ? `${d}d ${h % 24}h` : `${d}d`
}

/** "waiting 4m" for an item created at `since`, or '' when unknown. */
export function waitedLabel(since, now = Date.now()) {
  const t = toMillis(since)
  if (t == null) return ''
  return i18nOr('ui6Orgs.waiting_for', { duration: formatDuration(now - t) })
}

/**
 * Idle-stop countdown. `seconds` is idle_stop_in_seconds as reported at
 * `fetchedAt`; returns null when there is no countdown (null seconds).
 */
export function idleStopRemainingMs(seconds, fetchedAt, now = Date.now()) {
  if (seconds == null || Number.isNaN(Number(seconds))) return null
  return Math.max(0, Number(seconds) * 1000 - (now - fetchedAt))
}

export function idleStopLabel(remainingMs) {
  if (remainingMs == null) return ''
  if (remainingMs <= 0) return i18nOr('ui6Orgs.idle_stop_now')
  return i18nOr('ui6Orgs.idle_stop_in', { duration: formatDuration(remainingMs) })
}
