import { describe, expect, it } from 'vitest'
import { missionItems, sessionOf, stripFor, stripActions, workingOn, isTerminal, integrationTasks, hasFinalReport, pickReportTask, startedProposals } from './missionModel.js'

const plan = (sessionId, round = 0) => ({ type: 'mission.plan', payload: { sessionId, title: 'T', round } })
const propose = (title) => ({ type: 'mission.propose', payload: { title, goal: 'g' } })
const report = (sessionId) => ({ type: 'mission.report', payload: { sessionId, summary: 's' } })

describe('missionItems', () => {
  it('shows only the latest plan of a session, in the turn that sent it', () => {
    const out = missionItems([
      { key: 't1', state: { missions: [plan(7, 0)] } },
      { key: 't2', state: { missions: [plan(7, 1), plan(7, 2), plan(8, 0)] } },
    ])
    expect(out.t1).toBeUndefined()
    expect(out.t2.map(i => [i.type, i.payload.sessionId, i.payload.round])).toEqual([['plan', 7, 2], ['plan', 8, 0]])
  })
  it('keeps every proposal and the latest report per session', () => {
    const out = missionItems([
      { key: 't1', state: { missions: [propose('a'), report(7)] } },
      { key: 't2', state: { missions: [propose('b'), report(7)] } },
    ])
    expect(out.t1.map(i => i.type)).toEqual(['propose'])
    expect(out.t2.map(i => i.type)).toEqual(['propose', 'report'])
  })
  it('ignores turns without mission events', () => {
    expect(missionItems([{ key: 't', state: {} }, { key: 'u', state: null }])).toEqual({})
  })
})

describe('sessionOf and the strip', () => {
  const s = (id, conv, state, extra = {}) => ({ id, conversation_id: conv, state, archived: false, ...extra })
  it('finds the newest going session of this conversation only', () => {
    const list = [s(1, 'c1', 'done'), s(2, 'c1', 'running'), s(3, 'c2', 'running')]
    expect(sessionOf(list, 'c1').id).toBe(2)
    expect(sessionOf(list, 'c9')).toBeNull()
    expect(sessionOf(list, '')).toBeNull()
  })
  it('falls back to a finished one, never an archived one', () => {
    expect(sessionOf([s(1, 'c1', 'done')], 'c1').id).toBe(1)
    expect(sessionOf([s(1, 'c1', 'running', { archived: true })], 'c1')).toBeNull()
  })
  it('shows for non-terminal sessions, and for a done one only once reported', () => {
    expect(stripFor(s(1, 'c', 'running'), false)).toBe(true)
    expect(stripFor(s(1, 'c', 'draft'), false)).toBe(true)
    expect(stripFor(s(1, 'c', 'stopped'), true)).toBe(false)
    expect(stripFor(s(1, 'c', 'done'), false)).toBe(false)
    expect(stripFor(s(1, 'c', 'done'), true)).toBe(true)
    expect(stripFor(null, true)).toBe(false)
    expect(isTerminal(s(1, 'c', 'failed'))).toBe(true)
  })
  it('offers the buttons the state allows', () => {
    expect(stripActions(s(1, 'c', 'running'))).toEqual(['pause', 'stop'])
    expect(stripActions(s(1, 'c', 'paused'))).toEqual(['resume', 'stop'])
    expect(stripActions(s(1, 'c', 'done'))).toEqual([])
  })
  it('lists the tasks in progress with their workers', () => {
    expect(workingOn({ tasks: { in_progress: [{ id: 4, title: 'x', assignee: 'w1' }, { id: 5, title: 'y', claim: { by: 'w2' } }] } }))
      .toEqual([{ id: 4, title: 'x', worker: 'w1' }, { id: 5, title: 'y', worker: 'w2' }])
    expect(workingOn(null)).toEqual([])
  })
})

describe('pickReportTask', () => {
  const tasks = [
    { id: 1, kind: 'epic', title: 'Build' },
    { id: 2, kind: 'check', title: 'Unit checks' },
    { id: 3, kind: 'check', title: 'Integration check' },
  ]
  const fr = { id: 9, kind: 'report', ref: 'final-report', note: 'x' }
  it('finds the integration check tasks only', () => {
    expect(integrationTasks(tasks).map(t => t.id)).toEqual([3])
    expect(integrationTasks(null)).toEqual([])
  })
  it('recognises a final report by ref or kind, also in the untrusted view', () => {
    expect(hasFinalReport([fr])).toBe(true)
    expect(hasFinalReport([{ kind: 'final-report' }])).toBe(true)
    expect(hasFinalReport([{ kind: 'report', ref_untrusted: 'final-report' }])).toBe(true)
    expect(hasFinalReport([{ kind: 'report', ref: 'report:w1' }])).toBe(false)
    expect(hasFinalReport(undefined)).toBe(false)
  })
  it('prefers the task that has the final-report artifact', () => {
    expect(pickReportTask(tasks, { 2: [fr], 3: [] })).toEqual({ taskId: 2, hasReport: true })
  })
  it('falls back to the Integration check task, then to nothing', () => {
    expect(pickReportTask(tasks, { 3: [] })).toEqual({ taskId: 3, hasReport: false })
    expect(pickReportTask([tasks[0]], {})).toBeNull()
    expect(pickReportTask(null, null)).toBeNull()
  })
})

describe('startedProposals', () => {
  const P = (id, at) => ({ type: 'propose', id, at, payload: {} })
  const L = (id) => ({ type: 'plan', id, payload: {} })
  const sess = (state, created_at, extra = {}) => ({ id: 1, state, created_at, conversation_id: 'c', ...extra })
  it('a plan after a proposal starts it, a later proposal stays open', () => {
    const got = startedProposals([P('a'), L('p'), P('b')], new Set(), [], 'c')
    expect([...got]).toEqual(['a'])
  })
  it('a confirmed proposal is started', () => {
    expect([...startedProposals([P('a')], new Set(['a']), [], 'c')]).toEqual(['a'])
  })
  it('an open session of this conversation created after the proposal starts it; other chats and finished ones do not', () => {
    const items = [P('a', '2026-10-25T10:00:00Z')]
    expect(startedProposals(items, new Set(), [sess('running', '2026-10-25T10:05:00Z')], 'c').has('a')).toBe(true)
    expect(startedProposals(items, new Set(), [sess('stopped', '2026-10-25T10:05:00Z')], 'c').has('a')).toBe(false)
    expect(startedProposals(items, new Set(), [sess('running', '2026-10-25T10:05:00Z', { conversation_id: 'x' })], 'c').has('a')).toBe(false)
    expect(startedProposals(items, new Set(), [sess('running', '2026-10-25T09:00:00Z')], 'c').has('a')).toBe(false)
  })
})
