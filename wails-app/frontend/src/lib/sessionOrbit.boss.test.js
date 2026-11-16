import { describe, it, expect } from 'vitest'
import { orbitOf, bossOf, activeBoss, frontDeskOf, runRoster } from './sessionOrbit.js'

const S = (id, parent = 0, extra = {}) => ({ id, parent_session_id: parent, conversation_id: `c${id}`, title: `S${id}`, state: 'running', counts: {}, ...extra })
const B = (id, front, extra = {}) => S(id, 0, { title: 'Boss', boss: true, client_id: `boss-for-${front}-1`, ...extra })

describe('the boss and the chat orbit', () => {
  it('is never a satellite of the chat it stands behind: the dock ring shows the chat\'s own agents only', () => {
    const o = orbitOf([S(1), S(2, 1), B(9, 'c1')], { conversationId: 'c1' })
    expect(o.root.id).toBe(1)
    expect(o.satellites.map(c => c.id)).toEqual([2])
    expect(o.total).toBe(1)
  })
  it('leaves a chat with only a boss with no orbit', () => {
    const o = orbitOf([B(9, 'chat-x')], { conversationId: 'chat-x' })
    expect(o.root).toBeNull()
    expect(o.satellites).toEqual([])
  })
  it('only the recorded, unarchived boss of its own chat counts', () => {
    const unstamped = B(9, 'c1', { boss: false })
    expect(bossOf([unstamped], 'c1')).toBeNull()
    expect(bossOf([B(9, 'c1', { archived: true })], 'c1')).toBeNull()
    expect(bossOf([B(9, 'c1')], 'c2')).toBeNull()
  })
})

describe('runRoster', () => {
  it('groups the run by role: the boss, the planners and the executors, from the sessions kind', () => {
    const list = [
      S(1), B(9, 'c1'),
      S(20, 9, { kind: 'direction', title: 'x' }), S(21, 9, { kind: 'research' }),
      S(30, 20, { kind: 'task', title: 'Planner' }), S(31, 1, { kind: 'board' }),
    ]
    const r = runRoster(list, 'c1')
    expect(r.boss.id).toBe(9)
    expect(r.planners.map(c => c.id)).toEqual([20, 21])
    expect(r.executors.map(c => c.id).sort()).toEqual([30, 31])
  })
  it('puts urgent states first inside a role and never lists archived sessions or the boss twice', () => {
    const list = [B(9, 'c1'), S(20, 9, { kind: 'task', state: 'done' }), S(21, 9, { kind: 'task', state: 'failed' }), S(22, 9, { kind: 'task', archived: true })]
    expect(runRoster(list, 'c1').executors.map(c => c.id)).toEqual([21, 20])
  })
  it('is empty when the chat has no recorded boss', () => {
    expect(runRoster([S(1), S(2, 1)], 'c1')).toEqual({ boss: null, planners: [], executors: [] })
    expect(runRoster(null, 'c1').boss).toBeNull()
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
