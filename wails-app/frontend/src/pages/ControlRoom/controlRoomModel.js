// Pure helpers of the control room: what a CLI document means, never what it may do. The contract is
// internal/cli/doomsday/controlroom_types.go (schema_version 1).

export const MIN_POLL_MS = 3000          // never faster than this
export const MAX_POLL_MS = 30000         // backoff ceiling
export const IDLE_POLL_MS = 15000        // off / no run: nothing is moving
export const FEED_PAGE = 50
export const FEED_KEEP = 300             // rows kept in memory
export const MAX_CLUSTERS = 20
export const RATE_MIN_SPAN_MS = 30000    // a rate needs this much time between polls
export const RATE_WINDOW_MS = 10 * 60 * 1000
export const MAX_SAMPLES = 200

export const FEED_CLASSES = ['run', 'approval', 'plan', 'ask_answer', 'grant', 'spend', 'park', 'finish', 'refusal', 'other']

// Strings from the CLI are short identifiers; clip anyway so a bad document cannot grow the DOM.
export function clip(v, n = 64) {
  const s = typeof v === 'string' ? v : (v == null ? '' : String(v))
  return s.length > n ? s.slice(0, n) + '…' : s
}
export const num = (v) => (Number.isFinite(v) ? v : 0)
export const feedClass = (c) => (FEED_CLASSES.includes(c) ? c : 'other')

// Micro-dollars as dollars: 2 decimals from a dollar up, 4 below.
export function formatMicro(micro) {
  const m = num(micro)
  // Rounded on whole micro-dollars so 2750 is $0.0028, not the float's $0.0027.
  return '$' + (Math.abs(m) >= 1e6 ? (m / 1e6).toFixed(2) : (Math.round(m / 100) / 10000).toFixed(4))
}

// The state the page draws, from the overview document:
//   error | off | no_run | ok, and for ok the chain: yes | unanchored | no | unknown.
// Only chain "yes" is healthy. "unanchored" shows the data (tree, spend, feed) under an amber banner; a missing or odd
// value is "unknown", never "yes". A read failure (available false, reason "error") is an error with its error_code,
// never off.
export const CHAINS = ['yes', 'unanchored', 'no', 'unknown']
export const ERROR_CODES = ['db_unavailable', 'read_timeout', 'read_failed']
export const hasData = (chain) => chain === 'yes' || chain === 'unanchored'

export function viewOf(doc) {
  if (!doc || doc.error || doc.schema_version !== 1) {
    return { state: 'error', message: clip(doc?.error || 'unreadable control room document', 200), chain: 'unknown' }
  }
  if (!doc.available && doc.reason === 'error') {
    const code = ERROR_CODES.includes(doc.error_code) ? doc.error_code : 'unknown'
    return { state: 'error', errorCode: code, chain: 'unknown' }
  }
  if (!doc.available) return { state: doc.reason === 'off' ? 'off' : 'no_run', chain: 'unknown' }
  const c = doc.integrity?.chain_verified
  const chain = CHAINS.includes(c) ? c : 'unknown'
  return { state: 'ok', chain, healthy: chain === 'yes' }
}

// Clusters largest first, bounded.
export function clusterRows(tree) {
  const list = Array.isArray(tree?.clusters) ? tree.clusters : []
  return list.slice(0, MAX_CLUSTERS).map(c => ({ kind: clip(c?.kind, 40) || '?', total: num(c?.total), live: num(c?.live) }))
}

export function spendState(spend) {
  const used = num(spend?.used_micro)
  const cap = spend?.cap_micro
  if (spend?.no_ceiling) return { kind: 'no_ceiling', used }
  if (typeof cap !== 'number' || cap <= 0) return { kind: 'no_budget', used }
  const frac = used / cap
  return { kind: frac > 1 ? 'over' : frac >= 0.8 ? 'near' : 'ok', used, cap, frac: Math.min(frac, 1) }
}

// An approximate spend rate in micro-dollars per hour from successive polls: used grew between the oldest
// and newest sample of the last RATE_WINDOW_MS, which are at least RATE_MIN_SPAN_MS apart. null otherwise.
export function approxRate(samples) {
  if (!samples || samples.length < 2) return null
  const last = samples[samples.length - 1]
  const inWindow = samples.filter(s => last.t - s.t <= RATE_WINDOW_MS)
  const first = inWindow[0]
  const span = last.t - first.t
  if (span < RATE_MIN_SPAN_MS || last.used < first.used) return null
  return ((last.used - first.used) / span) * 3600000
}

// Adds a sample; a different run or a used amount that went down starts over (the old numbers do not compare).
export function addSample(samples, sample) {
  const prev = samples[samples.length - 1]
  const base = prev && (prev.run !== sample.run || sample.used < prev.used) ? [] : samples
  const next = [...base, sample]
  return next.length > MAX_SAMPLES ? next.slice(next.length - MAX_SAMPLES) : next
}

// Newest first, no duplicate seq, bounded.
export function mergeFeed(prev, incoming) {
  const bySeq = new Map()
  for (const r of [...(incoming || []), ...(prev || [])]) if (r && Number.isFinite(r.seq) && !bySeq.has(r.seq)) bySeq.set(r.seq, r)
  return [...bySeq.values()].sort((a, b) => b.seq - a.seq).slice(0, FEED_KEEP)
}

// The wait before the next poll: MIN_POLL_MS after a good one, doubling per failure up to MAX_POLL_MS.
export function nextDelay(failures, state) {
  if (failures > 0) return Math.min(MAX_POLL_MS, MIN_POLL_MS * 2 ** failures)
  return state === 'off' || state === 'no_run' ? IDLE_POLL_MS : MIN_POLL_MS
}
