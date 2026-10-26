import { describe, expect, it } from 'vitest'
import { chatReducer, initialChatState } from './chatReducer.js'

const scoped = () => chatReducer(initialChatState(), { type: 'scope', scope: { conversationId: 'c', turnId: 't' } })
const ev = (seq, type, payload) => ({ type: 'event', event: { conversationId: 'c', turnId: 't', seq, at: '2026-10-25T10:00:00Z', type, payload } })

describe('mission events in the reducer', () => {
  it('keeps propose, plan and report in order', () => {
    let s = scoped()
    expect(s.missions).toEqual([])
    s = chatReducer(s, ev(1, 'mission.propose', { title: 'A', goal: 'g' }))
    s = chatReducer(s, ev(2, 'mission.plan', { sessionId: 3, title: 'A', taskCount: 4 }))
    s = chatReducer(s, ev(3, 'mission.report', { sessionId: 3, summary: 'done' }))
    expect(s.missions.map(m => m.type)).toEqual(['mission.propose', 'mission.plan', 'mission.report'])
    expect(s.missions[2].payload.summary).toBe('done')
  })
})
