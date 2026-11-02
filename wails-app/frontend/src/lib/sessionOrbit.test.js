import { describe, it, expect } from 'vitest'
import { orbitOf, orbitState, sizeStep, ringPositions, linkPath, cycleIndex, MAX_SATELLITES } from './sessionOrbit.js'

const S = (id, parent = 0, extra = {}) => ({ id, parent_session_id: parent, conversation_id: `c${id}`, title: `S${id}`, state: 'running', counts: {}, ...extra })
const kids = (n, parent = 1) => Array.from({ length: n }, (_, i) => S(100 + i, parent))

describe('orbitOf', () => {
  it('has no root for a chat that leads no session', () => {
    expect(orbitOf([S(1)], { conversationId: 'nope' })).toMatchObject({ root: null, satellites: [], overflow: [] })
    expect(orbitOf(null, { conversationId: 'c1' }).root).toBeNull()
  })
  it('0 children: a root and an empty ring', () => {
    const o = orbitOf([S(1)], { conversationId: 'c1' })
    expect(o.root.id).toBe(1)
    expect(o.satellites).toEqual([])
    expect(o.total).toBe(0)
  })
  it('1, 8, 9 and 30 children', () => {
    for (const [n, shown, over] of [[1, 1, 0], [8, 8, 0], [9, 8, 1], [30, 8, 22]]) {
      const o = orbitOf([S(1), ...kids(n)], { sessionId: 1 })
      expect(o.satellites).toHaveLength(Math.min(n, MAX_SATELLITES))
      expect(o.satellites.length).toBe(shown)
      expect(o.overflow.length).toBe(over)
      expect(o.total).toBe(n)
    }
  })
  it('puts waiting and failed circles first so the chip never hides them', () => {
    const list = [S(1), ...kids(10), S(200, 1, { state: 'draft' }), S(201, 1, { state: 'failed' })]
    const o = orbitOf(list, { sessionId: 1 })
    expect(o.satellites.slice(0, 2).map(c => c.id)).toEqual([200, 201])
    expect(o.overflow.every(c => c.state === 'running')).toBe(true)
  })
  it('shows the next level only: levels 3 and 4 hang off their own parent', () => {
    const chain = [S(1), S(2, 1), S(3, 2), S(4, 3)]
    expect(orbitOf(chain, { sessionId: 1 }).satellites.map(c => c.id)).toEqual([2])
    const two = orbitOf(chain, { sessionId: 2 })
    expect(two.level).toBe(2)
    expect(two.satellites.map(c => [c.id, c.level, c.childCount])).toEqual([[3, 3, 1]])
    expect(orbitOf(chain, { sessionId: 3 }).satellites.map(c => c.id)).toEqual([4])
  })
  it('a level 4 session has no ring, even with rows below it', () => {
    const chain = [S(1), S(2, 1), S(3, 2), S(4, 3), S(5, 4)]
    const o = orbitOf(chain, { sessionId: 4 })
    expect(o.level).toBe(4)
    expect(o.satellites).toEqual([])
  })
  it('counts a satellite\'s own agents in childCount', () => {
    const o = orbitOf([S(1), S(2, 1), S(3, 2), S(4, 2)], { sessionId: 1 })
    expect(o.satellites[0].childCount).toBe(2)
  })
  it('survives an orphan, a self parent and a ring of parents', () => {
    const list = [S(1, 9), S(2, 2), S(3, 4), S(4, 3)]
    for (const id of [1, 2, 3, 4]) expect(() => orbitOf(list, { sessionId: id })).not.toThrow()
    expect(orbitOf(list, { sessionId: 1 }).root.id).toBe(1)
  })
  it('skips archived sessions', () => {
    const o = orbitOf([S(1), S(2, 1, { archived: true }), S(3, 1)], { sessionId: 1 })
    expect(o.satellites.map(c => c.id)).toEqual([3])
  })
  it('a satellite without a conversation cannot open a chat; an untitled one gets its number', () => {
    const o = orbitOf([S(1), S(2, 1, { conversation_id: '', title: '  ' })], { sessionId: 1 })
    expect(o.satellites[0]).toMatchObject({ canOpen: false, title: '#2' })
  })
  it('the topmost session of a conversation is its root', () => {
    const o = orbitOf([S(5, 1, { conversation_id: 'x' }), S(1, 0, { conversation_id: 'x' }), S(6, 1)], { conversationId: 'x' })
    expect(o.root.id).toBe(1)
  })
})

describe('orbitState and sizeStep', () => {
  it('maps the states, an unknown one to idle', () => {
    expect(['running', 'paused', 'done', 'failed', 'stopped', 'draft', 'weird', undefined].map(s => orbitState({ state: s })))
      .toEqual(['running', 'paused', 'done', 'failed', 'stopped', 'waiting', 'idle', 'idle'])
    expect(orbitState(null)).toBe('idle')
  })
  it('a stopped session is its own state, listed after paused and before idle and done, never among the failures', () => {
    const list = [S(1), S(2, 1, { state: 'done' }), S(3, 1, { state: 'stopped' }), S(4, 1, { state: 'failed' }), S(5, 1, { state: 'paused' })]
    expect(orbitOf(list, { sessionId: 1 }).satellites.map(c => c.state)).toEqual(['failed', 'paused', 'stopped', 'done'])
  })
  it('a session with a person waiting is waiting whatever its state', () => {
    expect(orbitState({ state: 'running', waiting: 2 })).toBe('waiting')
  })
  it('has three sizes from spend or task count', () => {
    expect(sizeStep({})).toBe(1)
    expect(sizeStep({ spent_usd: 2 })).toBe(2)
    expect(sizeStep({ spent_usd: 9 })).toBe(3)
    expect(sizeStep({ counts: { total: 6 } })).toBe(2)
    expect(sizeStep({ spent_usd: 0.1, counts: { total: 40 } })).toBe(3)
  })
})

describe('geometry', () => {
  it('a full ring starts at the top and is evenly spaced', () => {
    const p = ringPositions(4, { radius: 10 })
    expect(p[0]).toMatchObject({ x: 0, y: -10 })
    expect(p[1]).toMatchObject({ x: 10, y: 0 })
    expect(p[2]).toMatchObject({ x: 0, y: 10 })
  })
  it('an arc faces its direction, one satellite in the middle', () => {
    expect(ringPositions(1, { radius: 10, arc: 180, facing: 180 })[0]).toMatchObject({ x: -10, y: 0 })
    const p = ringPositions(3, { radius: 10, arc: 180, facing: 180 })
    expect(p[0].y).toBeGreaterThan(9)
    expect(p[2].y).toBeLessThan(-9)
    expect(ringPositions(0)).toEqual([])
  })
  it('numbers an arc from its top to its bottom on either side', () => {
    for (const facing of [0, 180]) {
      const ys = ringPositions(5, { radius: 50, arc: 120, facing, topDown: true }).map(p => p.y)
      expect(ys).toEqual([...ys].sort((a, b) => a - b))
    }
  })
  it('a link starts at the centre and ends at the satellite', () => {
    expect(linkPath(30, 40)).toMatch(/^M0 0 Q[-\d.]+ [-\d.]+ 30 40$/)
  })
  it('cycles both ways and handles an empty ring', () => {
    expect(cycleIndex(0, -1, 5)).toBe(4)
    expect(cycleIndex(4, 1, 5)).toBe(0)
    expect(cycleIndex(0, 1, 0)).toBe(-1)
  })
})

describe('the optional waiting count on a session row', () => {
  const S = (id, parent, extra = {}) => ({ id, parent_session_id: parent, conversation_id: `c${id}`, title: `A${id}`, state: 'running', counts: {}, ...extra })
  it('is carried on the circle and makes a running agent waiting', () => {
    const o = orbitOf([S(1, 0), S(2, 1, { waiting: 2 }), S(3, 1)], { sessionId: 1 })
    expect(o.satellites[0]).toMatchObject({ id: 2, state: 'waiting', waiting: 2 })
    expect(o.satellites[1]).toMatchObject({ id: 3, state: 'running', waiting: 0 })
  })
})

