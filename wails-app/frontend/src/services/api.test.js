import { describe, it, expect, vi, beforeEach } from 'vitest'

// Mock the generated Wails bindings and runtime so api.js can be tested in
// isolation. Each binding is a vi.fn we can make resolve or reject per test.
vi.mock('../wailsjs/go/main/App', () => ({
  GetPeople: vi.fn(),
  GetDashboardStats: vi.fn(),
  GetSummary: vi.fn(),
  GetSummarySections: vi.fn(),
  GetOrgSummary: vi.fn(),
  ListWorkflows: vi.fn(),
  DeleteSession: vi.fn(),
  CreateChatConversation: vi.fn(),
  StartChatTurn: vi.fn(),
  SaveChatAttachment: vi.fn(),
  ReadWorkspaceImage: vi.fn(),
  StopChatTurn: vi.fn(),
  StopChatAgent: vi.fn(),
  ListChatConversations: vi.fn(),
  GetChatTurns: vi.fn(),
  GetChatEvents: vi.fn(),
  DeleteChatConversation: vi.fn(),
  GetWorkflow: vi.fn(),
  ListProfileDocuments: vi.fn(),
}))
// Real Wails EventsOn semantics: each call registers its own listener under
// eventName and returns a disposer that removes only that listener.
// EventsOff(eventName), by contrast, would remove ALL listeners for the
// name — that's the footgun issue #15 is about, so the mock below models
// EventsOn faithfully rather than stubbing it out.
const eventListeners = new Map()
function fakeEventsOn(eventName, callback) {
  const listeners = eventListeners.get(eventName) ?? []
  listeners.push(callback)
  eventListeners.set(eventName, listeners)
  return () => {
    const current = eventListeners.get(eventName) ?? []
    eventListeners.set(eventName, current.filter((cb) => cb !== callback))
  }
}
function fakeEventsEmit(eventName, ...args) {
  for (const cb of eventListeners.get(eventName) ?? []) cb(...args)
}

vi.mock('../wailsjs/runtime/runtime', () => ({
  EventsOn: vi.fn((name, cb) => fakeEventsOn(name, cb)),
  EventsOff: vi.fn(),
}))

import * as GoApp from '../wailsjs/go/main/App'
import { api, onApiError, onLogEntry, subscribeEvent, onChatEvent } from './api.js'

describe('api error handling', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('answers no profile with a marker, without a warning or a toast', async () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    const seen = vi.fn()
    const off = onApiError(seen)
    GoApp.GetSummary.mockRejectedValueOnce(new Error('no profile selected: create a profile first'))
    GoApp.ListWorkflows.mockRejectedValueOnce(new Error('no profile selected: create a profile first'))
    expect(await api.getSummary()).toEqual({ no_profile: true })
    expect(await api.listWorkflows()).toEqual([])
    expect(warn).not.toHaveBeenCalled()
    expect(seen).not.toHaveBeenCalled()
    off()
    warn.mockRestore()
  })

  it('keeps the section poll quiet while no profile is selected', async () => {
    const seen = vi.fn()
    const off = onApiError(seen)
    GoApp.GetSummarySections.mockResolvedValueOnce('{"error":"no profile selected: create a profile first"}')
    expect(await api.getSummarySections('hil')).toBeNull()
    expect(seen).not.toHaveBeenCalled()
    off()
  })

  it('degrades a failed read to its safe default', async () => {
    GoApp.GetPeople.mockRejectedValueOnce(new Error('db locked'))
    const result = await api.getPeople()
    expect(result).toBeNull() // getPeople falls back to null
  })

  it('broadcasts a failure on the error bus instead of swallowing it silently', async () => {
    const events = []
    const off = onApiError((detail) => events.push(detail))

    GoApp.GetDashboardStats.mockRejectedValueOnce(new Error('boom'))
    await api.getDashboardStats()

    expect(events).toHaveLength(1)
    expect(events[0].op).toBe('dashboard stats')
    expect(events[0].message).toContain('boom')
    off()
  })

  it('parses the CLI summary and turns its {error} shape into a reported failure', async () => {
    GoApp.GetSummary.mockResolvedValueOnce('{"v":1,"hil":{"total":2}}')
    expect(await api.getSummary()).toEqual({ v: 1, hil: { total: 2 } })

    const events = []
    const off = onApiError((detail) => events.push(detail))
    GoApp.GetOrgSummary.mockResolvedValueOnce('{"error":"monoagentcli not found"}')
    expect(await api.getOrgSummary(true)).toBeNull()
    expect(GoApp.GetOrgSummary).toHaveBeenLastCalledWith(true)
    expect(events[0].message).toContain('monoagentcli not found')
    off()
  })

  it('passes through a successful read unchanged', async () => {
    GoApp.ListWorkflows.mockResolvedValueOnce([{ id: 'wf1' }])
    const result = await api.listWorkflows()
    expect(result).toEqual([{ id: 'wf1' }])
  })

  it('does not intercept write-path methods (they propagate to the caller)', async () => {
    GoApp.DeleteSession.mockRejectedValueOnce(new Error('validation'))
    await expect(api.deleteSession('s1')).rejects.toThrow('validation')
  })
})

// Regression test for issue #15: EventsOff(eventName) removes every listener
// registered under that name, not just the caller's own. App.jsx and
// People.jsx both subscribe to 'workflow:complete' independently; unmounting
// or re-subscribing one must never silence the other.
describe('event subscription independence (issue #15)', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    eventListeners.clear()
    globalThis.window = { runtime: {} }
  })

  it('disposing one subscription leaves other subscribers to the same event intact', () => {
    const appReceived = []
    const peoplePageReceived = []

    const disposeAppListener = subscribeEvent('workflow:complete', (data) => appReceived.push(data))
    const disposePeoplePageListener = subscribeEvent('workflow:complete', (data) => peoplePageReceived.push(data))

    // Simulate navigating away from People.jsx (or a filter-driven re-subscribe)
    disposePeoplePageListener()

    fakeEventsEmit('workflow:complete', { workflow_id: '1' })

    expect(appReceived).toEqual([{ workflow_id: '1' }])
    expect(peoplePageReceived).toEqual([])

    disposeAppListener()
  })

  it('does not cross-cancel independently subscribed events with different names', () => {
    const logReceived = []
    const workflowReceived = []

    const disposeLog = onLogEntry((entry) => logReceived.push(entry))
    subscribeEvent('workflow:complete', (data) => workflowReceived.push(data))

    disposeLog()

    fakeEventsEmit('log:entry', 'should not arrive')
    fakeEventsEmit('workflow:complete', { workflow_id: '2' })

    expect(logReceived).toEqual([])
    expect(workflowReceived).toEqual([{ workflow_id: '2' }])
  })
})

// New chat bindings (interactive-agent-chat plan, Task 3): a synchronous
// {"error":"..."} JSON payload on failure must become a real promise
// rejection via parseStreamResult, not a resolved value the caller has to
// remember to check — the same convention the pre-event-sourced chat API
// used before this plan replaced it.
describe('new chat bindings', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    eventListeners.clear()
  })

  it('createChatConversation parses a successful conversation payload and forwards effort', async () => {
    GoApp.CreateChatConversation.mockResolvedValueOnce(JSON.stringify({ id: 'conv-1', backend: 'agent', effort: 'high' }))
    const conv = await api.createChatConversation('general', 'claude', 'sonnet', 'high')
    expect(GoApp.CreateChatConversation).toHaveBeenCalledWith('general', 'claude', 'sonnet', 'high')
    expect(conv).toEqual({ id: 'conv-1', backend: 'agent', effort: 'high' })
  })

  it('createChatConversation defaults effort to empty string', async () => {
    GoApp.CreateChatConversation.mockResolvedValueOnce(JSON.stringify({ id: 'conv-1', backend: 'agent' }))
    await api.createChatConversation('general', 'claude', '')
    expect(GoApp.CreateChatConversation).toHaveBeenCalledWith('general', 'claude', '', '')
  })

  it('createChatConversation rejects on the {error} shape instead of resolving it', async () => {
    GoApp.CreateChatConversation.mockResolvedValueOnce(JSON.stringify({ error: 'boom' }))
    await expect(api.createChatConversation('general', 'claude', '')).rejects.toThrow('boom')
  })

  it('startChatTurn passes through a business-status response (not an error) unchanged', async () => {
    GoApp.StartChatTurn.mockResolvedValueOnce(JSON.stringify({ ok: false, turnId: 'turn-2', status: 'busy' }))
    const res = await api.startChatTurn('conv-1', 'turn-2', 'hi', false, false)
    expect(res).toEqual({ ok: false, turnId: 'turn-2', status: 'busy' })
  })

  it('startChatTurn sends no attachments by default, and the given paths otherwise', async () => {
    GoApp.StartChatTurn.mockResolvedValue(JSON.stringify({ ok: true, turnId: 't', status: 'active' }))
    await api.startChatTurn('conv-1', 't', 'hi', false, false)
    expect(GoApp.StartChatTurn).toHaveBeenLastCalledWith('conv-1', 't', 'hi', false, false, [])
    await api.startChatTurn('conv-1', 't', 'hi', false, false, ['/spool/a.png'])
    expect(GoApp.StartChatTurn).toHaveBeenLastCalledWith('conv-1', 't', 'hi', false, false, ['/spool/a.png'])
  })

  it('saveChatAttachment and readWorkspaceImage pass through, rejecting on error', async () => {
    GoApp.SaveChatAttachment.mockResolvedValueOnce('/home/u/.monoagent/chat-spool/c/x/a.png')
    await expect(api.saveChatAttachment('c', 'a.png', 'AAAA')).resolves.toBe('/home/u/.monoagent/chat-spool/c/x/a.png')
    expect(GoApp.SaveChatAttachment).toHaveBeenCalledWith('c', 'a.png', 'AAAA')
    GoApp.ReadWorkspaceImage.mockRejectedValueOnce('a.png is outside this chat\'s folder')
    await expect(api.readWorkspaceImage('c', '/etc/a.png')).rejects.toMatch('outside')
  })

  it('startChatTurn rejects on a hard binding failure', async () => {
    GoApp.StartChatTurn.mockResolvedValueOnce(JSON.stringify({ error: 'chat supervisor not initialized' }))
    await expect(api.startChatTurn('conv-1', 'turn-1', 'hi', false, false)).rejects.toThrow('chat supervisor not initialized')
  })

  it('getChatEvents returns the items/hasMore/lastCommittedSeq payload', async () => {
    const payload = { items: [{ seq: 1, type: 'turn.started' }], hasMore: false, lastCommittedSeq: 1 }
    GoApp.GetChatEvents.mockResolvedValueOnce(JSON.stringify(payload))
    const res = await api.getChatEvents('conv-1', 'turn-1', 0, 200)
    expect(res).toEqual(payload)
    expect(GoApp.GetChatEvents).toHaveBeenCalledWith('conv-1', 'turn-1', 0, 200)
  })

  it('listChatConversations returns items/nextCursor', async () => {
    GoApp.ListChatConversations.mockResolvedValueOnce(JSON.stringify({ items: [{ id: 'conv-1' }], nextCursor: '' }))
    const res = await api.listChatConversations('', 50)
    expect(res).toEqual({ items: [{ id: 'conv-1' }], nextCursor: '' })
  })

  it('stopChatTurn and deleteChatConversation resolve their parsed payload', async () => {
    GoApp.StopChatTurn.mockResolvedValueOnce(JSON.stringify({ ok: true }))
    await expect(api.stopChatTurn('conv-1', 'turn-1')).resolves.toEqual({ ok: true })

    GoApp.DeleteChatConversation.mockResolvedValueOnce(JSON.stringify({ ok: true }))
    await expect(api.deleteChatConversation('conv-1')).resolves.toEqual({ ok: true })
  })

  it('stopChatAgent passes the worker and resolves the CLI result', async () => {
    GoApp.StopChatAgent.mockResolvedValueOnce(JSON.stringify({ ok: true, agent_id: 'w2', status: 'cancelled', requested: true }))
    await expect(api.stopChatAgent('conv-1', 'turn-1', 'w2')).resolves.toMatchObject({ status: 'cancelled' })
    expect(GoApp.StopChatAgent).toHaveBeenCalledWith('conv-1', 'turn-1', 'w2')

    GoApp.StopChatAgent.mockResolvedValueOnce(JSON.stringify({ ok: false, error: 'boom' }))
    await expect(api.stopChatAgent('conv-1', 'turn-1', 'w2')).rejects.toThrow('boom')
  })

  it('onChatEvent subscribes under the chat:event name', () => {
    globalThis.window = { runtime: {} }
    const received = []
    const off = onChatEvent((e) => received.push(e))
    fakeEventsEmit('chat:event', { type: 'turn.started' })
    expect(received).toEqual([{ type: 'turn.started' }])
    off()
  })

  // getWorkflow/listProfileDocuments back chatArtifacts.js's resolveArtifact
  // — both are typed Go returns (no JSON string to parse), so the only
  // thing worth testing here is that a Go-side rejection degrades to the
  // guard()'d fallback instead of throwing.
  it('getWorkflow resolves the typed workflow object directly (no JSON.parse)', async () => {
    GoApp.GetWorkflow.mockResolvedValueOnce({ id: 'wf-1', name: 'My Workflow' })
    await expect(api.getWorkflow('wf-1')).resolves.toEqual({ id: 'wf-1', name: 'My Workflow' })
  })

  it('getWorkflow degrades to null for a deleted/cross-profile id instead of throwing', async () => {
    GoApp.GetWorkflow.mockRejectedValueOnce(new Error('workflow wf-1 not found'))
    await expect(api.getWorkflow('wf-1')).resolves.toBeNull()
  })

  it('listProfileDocuments resolves the typed document array directly', async () => {
    GoApp.ListProfileDocuments.mockResolvedValueOnce([{ id: 'doc-001', filename: 'a.md' }])
    await expect(api.listProfileDocuments()).resolves.toEqual([{ id: 'doc-001', filename: 'a.md' }])
  })

  it('listProfileDocuments degrades to [] on failure instead of throwing', async () => {
    GoApp.ListProfileDocuments.mockRejectedValueOnce(new Error('boom'))
    await expect(api.listProfileDocuments()).resolves.toEqual([])
  })
})
