// @vitest-environment jsdom
// Auto-dock (coder-bubble v2, Point 4): a running org gets its bubble in the
// dock without anything opening, whether the app started it, a bus tail saw
// it begin, or a schedule/daemon started it behind the app's back; one
// bubble per org; a closed bubble stays closed for the run; bubbles persist;
// rings read working, needs, done and error.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook, act, cleanup } from '@testing-library/react'
import sendEmail from './__fixtures__/hil-send-email.json'
import workflow from './__fixtures__/hil-workflow.json'

const { api, listeners, sub } = vi.hoisted(() => ({
  sub: set => cb => { set.add(cb); return () => set.delete(cb) },
  listeners: { org: new Set(), run: new Set(), closed: new Set(), chat: new Set() },
  api: {
    getOrgSummary: vi.fn(), getOrgStatus: vi.fn(), listNeedsYou: vi.fn(), getOrgLogs: vi.fn(), getOrgDesign: vi.fn(),
    streamOrgEvents: vi.fn(() => Promise.resolve({ ok: true })), stopOrgEvents: vi.fn(() => Promise.resolve({ ok: true })),
    getChatTurns: vi.fn(), hilList: vi.fn(),
  },
}))
vi.mock('../../services/api.js', () => ({
  api,
  onOrgEvent: sub(listeners.org),
  onOrgRunStatus: sub(listeners.run),
  onOrgEventsClosed: sub(listeners.closed),
  onChatEvent: sub(listeners.chat),
  newOrgEventsStreamId: (() => { let n = 0; return () => `s${++n}` })(),
}))

import { useCoderBubbles } from './useCoderBubbles.js'
import { useOrgBubbleWatch, WATCH_MS } from './useOrgBubbleWatch.js'
import { loadState } from '../../lib/coderBubbles.js'

function useDock() {
  const store = useCoderBubbles()
  const onCloseOrg = useOrgBubbleWatch(store)
  return { store, onCloseOrg }
}

const runStatus = (orgName, status) => act(() => { for (const cb of listeners.run) cb({ orgName, status }) })
const busEvent = (orgName, event) => act(() => { for (const cb of listeners.org) cb({ orgName, event }) })
const orgKeys = dock => dock.current.store.bubbles.map(b => b.key)
const poll = async () => { await act(async () => { await vi.advanceTimersByTimeAsync(WATCH_MS + 3_100) }) }
const rows = (...r) => ({ orgs: r.map(([name, running]) => ({ name, running })) })

beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: false })
  vi.clearAllMocks()
  for (const set of Object.values(listeners)) set.clear()
  localStorage.clear()
  api.getOrgSummary.mockResolvedValue(rows())
  api.getOrgStatus.mockResolvedValue({ run: 'r1' })
  api.listNeedsYou.mockResolvedValue({ items: [] })
  api.getOrgLogs.mockResolvedValue({ v: 1, items: [] })
  api.getOrgDesign.mockResolvedValue({ org: { roles: [{ id: 'ceo' }] } })
  api.getChatTurns.mockResolvedValue({ items: [] })
  api.hilList.mockResolvedValue([])
})
afterEach(() => { cleanup(); vi.useRealTimers() })

describe('auto-dock', () => {
  it('docks an org the app starts, collapsed, without opening anything', async () => {
    const { result } = renderHook(useDock)
    runStatus('acme', 'running')
    expect(orgKeys(result)).toEqual(['org:acme'])
    expect(result.current.store.expandedKey).toBe('')
  })

  it('never docks the same org twice, however it is told', async () => {
    api.getOrgSummary.mockResolvedValue(rows(['acme', true]))
    const { result } = renderHook(useDock)
    runStatus('acme', 'running')
    runStatus('acme', 'running')
    busEvent('acme', { id: 'e1', ts: 1, org: 'acme', run: 'r1', type: 'status', msg: 'org started (goal: x)' })
    await poll()
    // One bubble, now keyed by the run `org status` names.
    expect(orgKeys(result)).toEqual(['org:acme#r1'])
  })

  it('docks a run a bus tail sees begin', () => {
    const { result } = renderHook(useDock)
    busEvent('nightly', { id: 'e1', ts: 1, org: 'nightly', run: 'r1', type: 'status', msg: 'org started (goal: x)' })
    expect(orgKeys(result)).toEqual(['org:nightly'])
    // Other bus events don't.
    busEvent('other', { id: 'e2', ts: 2, org: 'other', run: 'r1', type: 'chat', from: 'ceo', msg: 'hi' })
    expect(orgKeys(result)).toEqual(['org:nightly'])
  })

  it('docks a scheduled or daemon run it cannot see, on the next poll', async () => {
    api.getOrgSummary.mockResolvedValue(rows(['nightly', true], ['idle', false]))
    const { result } = renderHook(useDock)
    expect(orgKeys(result)).toEqual([])
    await poll()
    expect(orgKeys(result)).toEqual(['org:nightly'])
    expect(result.current.store.expandedKey).toBe('')
  })

  it('persists docked bubbles like coder bubbles', async () => {
    const { result } = renderHook(useDock)
    runStatus('acme', 'running')
    await act(async () => {})
    expect(loadState().bubbles.map(b => b.key)).toEqual(['org:acme'])
    cleanup()
    const again = renderHook(useDock)
    await act(async () => {})
    expect(orgKeys(again.result)).toEqual(['org:acme'])
    expect(result).toBeTruthy()
  })
})

describe('closing and stopping', () => {
  it('keeps a bubble closed for the rest of its run, and docks the next run again', async () => {
    api.getOrgSummary.mockResolvedValue(rows(['acme', true]))
    const { result } = renderHook(useDock)
    runStatus('acme', 'running')
    act(() => { result.current.onCloseOrg('acme', 0); result.current.store.close('org:acme') })
    expect(orgKeys(result)).toEqual([])
    await poll()
    expect(orgKeys(result)).toEqual([])
    busEvent('acme', { id: 'e1', ts: 1, org: 'acme', run: 'r1', type: 'status', msg: 'org started (goal: x)' })
    expect(orgKeys(result)).toEqual([])
    // The org stops, then starts again: a new run docks.
    runStatus('acme', 'stopped')
    runStatus('acme', 'running')
    expect(orgKeys(result)).toEqual(['org:acme'])
  })

  it('brings a closed bubble back when more items wait than when it was closed', async () => {
    api.getOrgSummary.mockResolvedValue(rows(['acme', true]))
    api.listNeedsYou.mockResolvedValue({ items: [{}, {}] })
    const { result } = renderHook(useDock)
    runStatus('acme', 'running')
    act(() => { result.current.onCloseOrg('acme', 1); result.current.store.close('org:acme') })
    await poll()
    expect(orgKeys(result)).toEqual(['org:acme'])
  })

  it('leaves a stopped org\'s bubble in the dock until it is closed', async () => {
    const { result } = renderHook(useDock)
    runStatus('acme', 'running')
    runStatus('acme', 'stopped')
    api.getOrgSummary.mockResolvedValue(rows(['acme', false]))
    await poll()
    expect(orgKeys(result)).toEqual(['org:acme'])
  })
})

describe('rings', () => {
  const ev = (id, ts, extra) => ({ id, ts, org: 'acme', run: 'r1', ...extra })

  it('reads working, then done, with the finished run unread while collapsed', async () => {
    const { result } = renderHook(useDock)
    runStatus('acme', 'running')
    await act(async () => {})
    const ring = () => result.current.store.summaryOf('org:acme')
    busEvent('acme', ev('a', 1, { type: 'status', msg: 'org started (goal: x)' }))
    busEvent('acme', ev('b', 2, { type: 'status', from: 'ceo', reason: 'state-change', data: { from: 'idle', to: 'working' } }))
    expect(ring().status).toBe('working')
    busEvent('acme', ev('c', 3, { type: 'status', msg: 'org stopped' }))
    expect(ring().status).toBe('done')
    expect(ring().unread).toBe(1)
  })

  it('reads error when a run ended with errors', async () => {
    const { result } = renderHook(useDock)
    runStatus('acme', 'running')
    await act(async () => {})
    busEvent('acme', ev('a', 1, { type: 'status', msg: 'org started (goal: x)' }))
    busEvent('acme', ev('b', 2, { type: 'audit', reason: 'session-result-error', from: 'dev' }))
    busEvent('acme', ev('c', 3, { type: 'status', msg: 'org stopped' }))
    expect(result.current.store.summaryOf('org:acme').status).toBe('error')
  })

  it('reads needs while a workflow approval or a send waits, though the bus never said so', async () => {
    api.getOrgSummary.mockResolvedValue(rows(['acme', true]))
    api.hilList.mockResolvedValue([workflow, sendEmail])
    const { result } = renderHook(useDock)
    runStatus('acme', 'running')
    await poll()
    const ring = result.current.store.summaryOf('org:acme')
    expect(ring.status).toBe('needs')
    // Only the workflow item is tagged acme; the send names a role the dock doesn't know.
    expect(ring.needs).toBe(1)
    // Once the item is answered the ring settles.
    api.hilList.mockResolvedValue([])
    await poll()
    expect(result.current.store.summaryOf('org:acme').status).not.toBe('needs')
  })

  it('rings a coder bubble amber for a send its chat prepared', async () => {
    api.hilList.mockResolvedValue([sendEmail])
    const { result } = renderHook(useDock)
    act(() => { result.current.store.openConversation({ id: 'c1', cwd: '/w/x' }) })
    await poll()
    expect(result.current.store.summaryOf('c1')).toMatchObject({ status: 'needs', needs: 1 })
  })
})
