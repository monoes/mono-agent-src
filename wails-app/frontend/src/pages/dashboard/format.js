import i18nOr from '../../lib/i18nOr.js'

// Time labels for the dashboard. `t` is react-i18next's t; `now` is
// injectable for tests.
export function relTime(ts, t, now = Date.now()) {
  if (!ts) return '—'
  const diff = now - new Date(ts)
  if (isNaN(diff)) return '—'
  if (diff < 60000) return t('dashboard.time.justNow')
  if (diff < 3600000) return t('dashboard.time.minutesAgo', { count: Math.floor(diff / 60000) })
  if (diff < 86400000) return t('dashboard.time.hoursAgo', { count: Math.floor(diff / 3600000) })
  return new Date(ts).toLocaleDateString()
}

export function duration(startedAt, finishedAt, now = Date.now()) {
  if (!startedAt) return null
  const ms = (finishedAt ? new Date(finishedAt) : now) - new Date(startedAt)
  if (isNaN(ms) || ms < 0) return null
  const sec = Math.round(ms / 1000)
  return sec < 60 ? `${sec}s` : `${Math.floor(sec / 60)}m ${sec % 60}s`
}

// execStatusWord is the shown word for a stored execution status; one this app
// does not know is shown as stored.
const EXEC_STATUS_KEYS = {
  SUCCESS: 'ui6PagesMisc.exec_SUCCESS',
  SUCCESS_WITH_ERRORS: 'ui6PagesMisc.exec_SUCCESS_WITH_ERRORS',
  COMPLETED: 'ui6PagesMisc.exec_COMPLETED',
  RUNNING: 'ui6PagesMisc.exec_RUNNING',
  QUEUED: 'ui6PagesMisc.exec_QUEUED',
  PENDING: 'ui6PagesMisc.exec_PENDING',
  WAITING: 'ui6PagesMisc.exec_WAITING',
  PAUSED: 'ui6PagesMisc.exec_PAUSED',
  FAILED: 'ui6PagesMisc.exec_FAILED',
  CANCELLED: 'ui6PagesMisc.exec_CANCELLED',
}
export const execStatusWord = (status) => (Object.hasOwn(EXEC_STATUS_KEYS, status) ? i18nOr(EXEC_STATUS_KEYS[status]) : status)

export function untilTime(ts, t, now = Date.now()) {
  const diff = new Date(ts) - now
  if (isNaN(diff)) return '—'
  if (diff < 60000) return t('dashboard.time.now')
  if (diff < 3600000) return t('dashboard.time.inMinutes', { count: Math.round(diff / 60000) })
  if (diff < 86400000) return t('dashboard.time.inHours', { count: Math.round(diff / 3600000) })
  return new Date(ts).toLocaleString()
}
