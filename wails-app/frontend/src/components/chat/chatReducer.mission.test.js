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

describe('ui events in the reducer', () => {
  const msg = m => ({ version: '1.0', ...m })
  it('folds ui.surface into state.surfaces in place and marks ui.action', () => {
    let s = scoped()
    s = chatReducer(s, ev(1, 'assistant.delta', { partId: 'p1', text: 'hi' }))
    s = chatReducer(s, ev(2, 'ui.surface', { surfaceId: 'a', provenance: 'agent', message: msg({ createSurface: { surfaceId: 'a' } }) }))
    s = chatReducer(s, ev(3, 'ui.surface', { surfaceId: 'a', provenance: 'system', message: msg({ updateDataModel: { surfaceId: 'a', path: '/x', value: 1 } }) }))
    expect(s.surfaces).toHaveLength(1)
    expect(s.surfaces[0]).toMatchObject({ partIndex: 1, dataModel: { x: 1 }, status: 'live' })
    s = chatReducer(s, ev(4, 'ui.action', { surfaceId: 'a', name: 'n', outcome: 'accepted' }))
    expect(s.surfaces[0].status).toBe('resolved')
    s = chatReducer(s, ev(5, 'ui.surface', { surfaceId: 'a', message: msg({ deleteSurface: { surfaceId: 'a' } }) }))
    expect(s.surfaces).toEqual([])
  })
})

describe('journal turns across a conversation', () => {
  const msg = m => ({ version: '1.0', ...m })
  const card = (id, provenance, label) => ({ surfaceId: id, provenance, message: msg({ createSurface: { surfaceId: id, components: [{ id: 'root', component: 'Text', text: label }] } }) })
  const turn = (key, at, evs) => ({ key, state: evs.reduce((s, [type, payload], i) => chatReducer(s, { type: 'event', event: { conversationId: 'c', turnId: 't', seq: i + 1, at, type, payload } }), scoped()) })
  const T = n => `2026-11-25T10:00:0${n}Z`

  it('puts a decided notice where the card was, and keeps system cards live after a press', async () => {
    const { foldConversationSurfaces, isJournalTurn } = await import('../../lib/a2uiModel.js')
    const made = turn('uisurface-1', T(1), [['ui.surface', card('hil-1', 'system', 'old')]])
    const notice = turn('uisurface-2', T(3), [
      ['ui.surface', { surfaceId: 'hil-1', provenance: 'system', message: msg({ deleteSurface: { surfaceId: 'hil-1' } }) }],
      ['ui.surface', card('hil-1', 'system', 'Approved by you')]])
    const press = turn('uiaction-3', T(4), [['ui.action', { surfaceId: 'hil-1', outcome: 'accepted' }]])
    const out = foldConversationSurfaces([made, notice, press])
    expect(Object.keys(out)).toEqual(['uisurface-1'])
    expect(out['uisurface-1'][0].components.root.text).toBe('Approved by you')
    expect(out['uisurface-1'][0].status).toBe('live')
    expect(isJournalTurn('uiaction-3') && isJournalTurn('uisurface-2')).toBe(true)
    expect(isJournalTurn('turn-1')).toBe(false)
  })

  it('settles an agent card pressed in a later turn, until the agent draws it again', async () => {
    const { foldConversationSurfaces } = await import('../../lib/a2uiModel.js')
    const made = turn('t1', T(1), [['ui.surface', card('q', 'agent', 'first')]])
    const press = turn('uiaction-2', T(2), [['ui.action', { surfaceId: 'q', outcome: 'accepted' }]])
    expect(foldConversationSurfaces([made, press]).t1[0].status).toBe('resolved')
    const rejected = turn('uiaction-2', T(2), [['ui.action', { surfaceId: 'q', outcome: 'rejected' }]])
    expect(foldConversationSurfaces([made, rejected]).t1[0].status).toBe('live')
    const again = turn('t3', T(3), [['ui.surface', card('q', 'agent', 'second')]])
    const out = foldConversationSurfaces([made, press, again])
    expect(out.t1[0]).toMatchObject({ status: 'live' })
    expect(out.t1[0].components.root.text).toBe('second')
  })

  it('applies a later turn data update and removal to an earlier card', async () => {
    const { foldConversationSurfaces } = await import('../../lib/a2uiModel.js')
    const made = turn('t1', T(1), [['ui.surface', card('m', 'system', 'plan')]])
    const upd = turn('t2', T(2), [['ui.surface', { surfaceId: 'm', message: msg({ updateDataModel: { surfaceId: 'm', path: '/n', value: 2 } }) }]])
    expect(foldConversationSurfaces([made, upd]).t1[0].dataModel).toEqual({ n: 2 })
    const gone = turn('t3', T(3), [['ui.surface', { surfaceId: 'm', message: msg({ deleteSurface: { surfaceId: 'm' } }) }]])
    expect(foldConversationSurfaces([made, upd, gone])).toEqual({})
  })
})
