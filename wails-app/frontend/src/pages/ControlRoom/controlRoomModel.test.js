import { describe, it, expect } from 'vitest'
import {
  viewOf, clusterRows, spendState, approxRate, addSample, mergeFeed, nextDelay, formatMicro, clip, feedClass,
  MIN_POLL_MS, MAX_POLL_MS, IDLE_POLL_MS, MAX_CLUSTERS, FEED_KEEP, RATE_MIN_SPAN_MS,
} from './controlRoomModel.js'

describe('viewOf', () => {
  it('reads each state; only chain "yes" is healthy', () => {
    expect(viewOf(null).state).toBe('error')
    expect(viewOf({ error: 'x' }).state).toBe('error')
    expect(viewOf({ schema_version: 2, available: true }).state).toBe('error')
    expect(viewOf({ schema_version: 1, available: false, reason: 'off' }).state).toBe('off')
    expect(viewOf({ schema_version: 1, available: false, reason: 'no_run' }).state).toBe('no_run')
    expect(viewOf({ schema_version: 1, available: true, integrity: { chain_verified: 'yes' } })).toMatchObject({ state: 'ok', chain: 'yes', healthy: true })
    expect(viewOf({ schema_version: 1, available: true, integrity: { chain_verified: 'no' } })).toMatchObject({ chain: 'no' })
    for (const odd of [undefined, 'maybe', 'YES', true]) {
      const v = viewOf({ schema_version: 1, available: true, integrity: { chain_verified: odd } })
      expect(v.chain).toBe('unknown')
      expect(v.healthy).toBeFalsy()
    }
    expect(viewOf({ schema_version: 1, available: true }).chain).toBe('unknown')
  })
})

describe('viewOf: the integrity states and read errors', () => {
  const doc = (c) => ({ schema_version: 1, available: true, integrity: { chain_verified: c } })
  it('only yes is healthy; unanchored has data but is not healthy', () => {
    expect(viewOf(doc('yes'))).toMatchObject({ state: 'ok', chain: 'yes', healthy: true })
    expect(viewOf(doc('unanchored'))).toMatchObject({ state: 'ok', chain: 'unanchored', healthy: false })
    expect(viewOf(doc('no'))).toMatchObject({ chain: 'no', healthy: false })
    expect(viewOf(doc('maybe'))).toMatchObject({ chain: 'unknown', healthy: false })
  })
  it('reason error is an error with its code, never off', () => {
    const v = viewOf({ schema_version: 1, available: false, reason: 'error', error_code: 'read_timeout' })
    expect(v).toMatchObject({ state: 'error', errorCode: 'read_timeout' })
    expect(viewOf({ schema_version: 1, available: false, reason: 'error', error_code: 'x' }).errorCode).toBe('unknown')
    expect(viewOf({ schema_version: 1, available: false, reason: 'off' }).state).toBe('off')
  })
})

describe('bounded output', () => {
  it('clusters are capped and sanitised', () => {
    const rows = clusterRows({ clusters: Array.from({ length: 5000 }, (_, i) => ({ kind: 'k' + i, total: i, live: 'x' })) })
    expect(rows).toHaveLength(MAX_CLUSTERS)
    expect(rows[0].live).toBe(0)
    expect(clusterRows(null)).toEqual([])
  })
  it('clip and feedClass', () => {
    expect(clip('a'.repeat(500), 10)).toHaveLength(11)
    expect(clip(undefined)).toBe('')
    expect(feedClass('grant')).toBe('grant')
    expect(feedClass('<script>')).toBe('other')
  })
  it('mergeFeed dedupes, sorts newest first and keeps FEED_KEEP rows', () => {
    const rows = Array.from({ length: FEED_KEEP + 50 }, (_, i) => ({ seq: i + 1 }))
    const out = mergeFeed(rows.slice(0, 100), rows.slice(50))
    expect(out).toHaveLength(FEED_KEEP)
    expect(out[0].seq).toBe(FEED_KEEP + 50)
    expect(new Set(out.map(r => r.seq)).size).toBe(out.length)
    expect(mergeFeed([{ seq: 1 }], [{ seq: NaN }, null, { seq: 2 }]).map(r => r.seq)).toEqual([2, 1])
  })
  it('formats money', () => {
    expect(formatMicro(3000)).toBe('$0.0030')
    expect(formatMicro(2750)).toBe('$0.0028')
    expect(formatMicro(5_000_000)).toBe('$5.00')
    expect(formatMicro(undefined)).toBe('$0.0000')
  })
})

describe('spendState', () => {
  it('names the states', () => {
    expect(spendState({ no_ceiling: true, used_micro: 5 }).kind).toBe('no_ceiling')
    expect(spendState({ cap_micro: null, used_micro: 5 }).kind).toBe('no_budget')
    expect(spendState({ cap_micro: 100, used_micro: 10 })).toMatchObject({ kind: 'ok', frac: 0.1 })
    expect(spendState({ cap_micro: 100, used_micro: 80 }).kind).toBe('near')
    expect(spendState({ cap_micro: 100, used_micro: 150 })).toMatchObject({ kind: 'over', frac: 1 })
  })
})

describe('approxRate', () => {
  const s = (t, used, run = 'r') => ({ t, used, run })
  it('needs two readings far enough apart', () => {
    expect(approxRate([])).toBeNull()
    expect(approxRate([s(0, 0)])).toBeNull()
    expect(approxRate([s(0, 0), s(RATE_MIN_SPAN_MS - 1, 10)])).toBeNull()
  })
  it('is micro-dollars per hour over the window', () => {
    expect(approxRate([s(0, 0), s(5 * 60_000, 500)])).toBeCloseTo(6000)
    expect(approxRate([s(0, 7), s(60_000, 7)])).toBe(0)
  })
  it('uses only the last window', () => {
    expect(approxRate([s(0, 0), s(20 * 60_000, 1000), s(21 * 60_000, 1000)])).toBe(0)
  })
  it('starts over when the run changes or used goes down', () => {
    let a = addSample([], s(0, 5))
    a = addSample(a, s(1, 9))
    expect(a).toHaveLength(2)
    expect(addSample(a, s(2, 3))).toHaveLength(1)
    expect(addSample(a, s(2, 20, 'other'))).toHaveLength(1)
  })
})

describe('nextDelay', () => {
  it('never polls faster than the minimum and backs off', () => {
    expect(nextDelay(0, 'ok')).toBe(MIN_POLL_MS)
    expect(MIN_POLL_MS).toBeGreaterThanOrEqual(3000)
    expect(nextDelay(1, 'error')).toBe(2 * MIN_POLL_MS)
    expect(nextDelay(2, 'error')).toBe(4 * MIN_POLL_MS)
    expect(nextDelay(50, 'error')).toBe(MAX_POLL_MS)
    expect(nextDelay(0, 'off')).toBe(IDLE_POLL_MS)
    expect(nextDelay(0, 'no_run')).toBe(IDLE_POLL_MS)
  })
})
