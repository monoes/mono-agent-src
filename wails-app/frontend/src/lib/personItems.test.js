import { describe, expect, it } from 'vitest'
import { CHIME_DEBOUNCE_MS, planAlert, trimSeen, withWaiting, itemsForConversation, budgetSharePercent, SEEN_KEEP } from './personItems.js'

const item = (id, extra = {}) => ({ id, kind: 'plan_approval', sessionId: id * 10, conversationId: `c${id}`, ...extra })

describe('planAlert', () => {
  it('chimes once for a new id and never again for it', () => {
    const a = planAlert({ items: [item(1)], seen: new Set(), lastChimeAt: 0, now: 100000 })
    expect(a.chime).toBe(true)
    expect(a.fresh.map(i => i.id)).toEqual([1])
    const b = planAlert({ items: [item(1)], seen: a.seen, lastChimeAt: a.lastChimeAt, now: 100000 + 10 * CHIME_DEBOUNCE_MS })
    expect(b.chime).toBe(false)
    expect(b.fresh).toEqual([])
  })

  it('chimes at most once per five seconds, and the leftover id stays silent', () => {
    const a = planAlert({ items: [item(1)], seen: new Set(), lastChimeAt: 0, now: 100000 })
    const b = planAlert({ items: [item(1), item(2)], seen: a.seen, lastChimeAt: a.lastChimeAt, now: 100000 + CHIME_DEBOUNCE_MS - 1 })
    expect(b.chime).toBe(false)
    expect(b.fresh.map(i => i.id)).toEqual([2])
    expect(b.seen.has(2)).toBe(true)
    const c = planAlert({ items: [item(1), item(2)], seen: b.seen, lastChimeAt: b.lastChimeAt, now: 100000 + 2 * CHIME_DEBOUNCE_MS })
    expect(c.chime).toBe(false)
    const d = planAlert({ items: [item(3)], seen: c.seen, lastChimeAt: a.lastChimeAt, now: 100000 + CHIME_DEBOUNCE_MS })
    expect(d.chime).toBe(true)
  })

  it('two new items in one poll are one chime', () => {
    const a = planAlert({ items: [item(1), item(2)], seen: new Set(), lastChimeAt: 0, now: 50000 })
    expect(a.chime).toBe(true)
    expect(a.fresh).toHaveLength(2)
  })

  it('is silent when sound is off but still reports the new items and marks them seen', () => {
    const a = planAlert({ items: [item(1)], seen: new Set(), lastChimeAt: 0, now: 50000, soundOn: false })
    expect(a.chime).toBe(false)
    expect(a.fresh).toHaveLength(1)
    expect(a.seen.has(1)).toBe(true)
    expect(a.lastChimeAt).toBe(0)
  })

  it('reads odd input as nothing new', () => {
    expect(planAlert({ items: null, seen: null, now: 1 }).chime).toBe(false)
    expect(planAlert({ items: [null, { kind: 'x' }], seen: [], now: 1 }).fresh).toEqual([])
  })
})

it('trimSeen keeps the newest ids', () => {
  const ids = new Set(Array.from({ length: SEEN_KEEP + 20 }, (_, i) => i + 1))
  const kept = trimSeen(ids)
  expect(kept).toHaveLength(SEEN_KEEP)
  expect(kept[kept.length - 1]).toBe(SEEN_KEEP + 20)
})

describe('orbit wiring', () => {
  const sessions = [{ id: 10, state: 'draft' }, { id: 20, state: 'running' }, { id: 99, state: 'running' }]
  it('sets waiting on the sessions that have an open item', () => {
    const out = withWaiting(sessions, [item(1), item(2)])
    expect(out.map(s => s.waiting)).toEqual([1, 1, undefined])
    expect(sessions[0].waiting).toBeUndefined()
  })
  it('returns the list untouched with no items', () => {
    expect(withWaiting(sessions, [])).toBe(sessions)
    expect(withWaiting(undefined, [item(1)])).toBeUndefined()
  })
})

it('itemsForConversation matches the agent chat only', () => {
  expect(itemsForConversation([item(1), item(2)], 'c2').map(i => i.id)).toEqual([2])
  expect(itemsForConversation([item(1)], '')).toEqual([])
})

it('budgetSharePercent', () => {
  expect(budgetSharePercent({ budgetShare: 0.456 })).toBe('46')
  expect(budgetSharePercent({})).toBe('')
})
