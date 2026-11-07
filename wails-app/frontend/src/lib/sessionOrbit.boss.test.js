import { describe, it, expect } from 'vitest'
import { orbitOf, bossOf, activeBoss, frontDeskOf, MAX_SATELLITES } from './sessionOrbit.js'

const S = (id, parent = 0, extra = {}) => ({ id, parent_session_id: parent, conversation_id: `c${id}`, title: `S${id}`, state: 'running', counts: {}, ...extra })
const B = (id, front, extra = {}) => S(id, 0, { title: 'Boss', boss: true, client_id: `boss-for-${front}-1`, ...extra })

describe('the boss in a chat orbit', () => {
  it('is the first satellite of the chat it stands behind, a sibling of the chat session', () => {
    const o = orbitOf([S(1), S(2, 1), B(9, 'c1')], { conversationId: 'c1' })
    expect(o.root.id).toBe(1)
    expect(o.satellites.map(c => [c.id, c.boss])).toEqual([[9, true], [2, false]])
    expect(o.satellites[0].conversationId).toBe('c9')
    expect(o.satellites[0].canOpen).toBe(true)
  })
  it('shows even before the chat has a session of its own', () => {
    const o = orbitOf([B(9, 'chat-x')], { conversationId: 'chat-x' })
    expect(o.root).toBeNull()
    expect(o.satellites.map(c => c.id)).toEqual([9])
  })
  it('belongs to its own chat only, and only when the operator recorded it', () => {
    expect(orbitOf([S(1), B(9, 'c1')], { conversationId: 'c2' }).satellites).toEqual([])
    const unstamped = B(9, 'c1', { boss: false })
    expect(orbitOf([S(1), unstamped], { conversationId: 'c1' }).satellites).toEqual([])
    expect(bossOf([unstamped], 'c1')).toBeNull()
    expect(bossOf([B(9, 'c1', { archived: true })], 'c1')).toBeNull()
  })
  it('takes one of the slots: the ring never grows past the maximum', () => {
    const kids = Array.from({ length: 12 }, (_, i) => S(100 + i, 1))
    const o = orbitOf([S(1), ...kids, B(9, 'c1')], { conversationId: 'c1' })
    expect(o.satellites).toHaveLength(MAX_SATELLITES)
    expect(o.satellites[0].boss).toBe(true)
    expect(o.overflow).toHaveLength(12 - (MAX_SATELLITES - 1))
    expect(o.total).toBe(13)
  })
  it('is not a satellite of its own session view', () => {
    expect(orbitOf([S(1), B(9, 'c1')], { sessionId: 1 }).satellites).toEqual([])
  })
})

describe('boss keys', () => {
  it('reads the chat back whatever the chat id looks like', () => {
    expect(frontDeskOf({ client_id: 'boss-for-11111111-2222-3333-4444-123456789012-3' })).toBe('11111111-2222-3333-4444-123456789012')
    for (const bad of ['', 'boss-for-x', 'boss-for-x-0', 'boss-for-x-1a', 'other-x-1']) expect(frontDeskOf({ client_id: bad })).toBe('')
  })
  it('takes the newest boss of a chat, and only an open one when asked', () => {
    const list = [B(3, 'c1', { state: 'done' }), B(5, 'c1', { client_id: 'boss-for-c1-2' })]
    expect(bossOf(list, 'c1').id).toBe(5)
    expect(bossOf([B(3, 'c1', { state: 'done' })], 'c1').id).toBe(3)
    expect(bossOf([B(3, 'c1', { state: 'done' })], 'c1', { open: true })).toBeNull()
  })
})

describe('activeBoss and frontDeskOf', () => {
  it('finds the newest open recorded boss', () => {
    expect(activeBoss([B(3, 'a', { state: 'done' }), B(5, 'b'), B(7, 'c', { archived: true }), S(6)]).id).toBe(5)
    expect(activeBoss([S(1)])).toBeNull()
    expect(activeBoss(null)).toBeNull()
  })
  it('reads the chat from the key', () => {
    expect(frontDeskOf(B(9, 'chat-x'))).toBe('chat-x')
    expect(frontDeskOf(S(1))).toBe('')
  })
})
