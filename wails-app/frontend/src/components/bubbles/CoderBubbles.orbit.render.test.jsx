// @vitest-environment jsdom
import React, { useEffect } from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, renderHook, screen, fireEvent, waitFor, cleanup, act, within } from '@testing-library/react'
import '../../i18n.js'

const { api, chatAccess, listeners, mockConfirm, sessions } = vi.hoisted(() => ({
  sessions: { list: vi.fn(), show: vi.fn(), board: vi.fn(), pulse: vi.fn(), pause: vi.fn(), resume: vi.fn(), stop: vi.fn() },
  listeners: { chat: new Set() },
  chatAccess: { create: vi.fn(), setAccess: vi.fn(), record: vi.fn() },
  mockConfirm: vi.fn(),
  api: {
    getChatTurns: vi.fn(),
    getChatEvents: vi.fn(),
    startChatTurn: vi.fn(),
    stopChatTurn: vi.fn(),
    coderStatus: vi.fn(),
    coderWorkspaceList: vi.fn(),
    coderWorkspaceDefault: vi.fn(),
    createCoderConversation: vi.fn(),
    getAgentRuntimeModels: vi.fn(),
    pickCoderFolder: vi.fn(),
    openPathWithOS: vi.fn(),
    getProfiles: vi.fn(),
  },
}))
vi.mock('../../services/api.js', () => ({
  api,
  notify: vi.fn(),
  onChatEvent: cb => { listeners.chat.add(cb); return () => listeners.chat.delete(cb) },
  onOrgEvent: () => () => {},
  onOrgRunStatus: () => () => {},
  newOrgEventsStreamId: () => 'stream',
}))
vi.mock('../../lib/chatAccessApi.js', () => ({ chatAccessApi: chatAccess }))
vi.mock('../../services/taskSessions.js', () => ({ sessionsApi: sessions }))
vi.mock('../ConfirmDialog.jsx', () => ({ confirm: (...a) => mockConfirm(...a) }))

import CoderBubbles from './CoderBubbles.jsx'
import { useCoderBubbles } from './useCoderBubbles.js'

let store
const navigate = vi.fn()
function Harness({ openRoot = true }) {
  store = useCoderBubbles()
  useEffect(() => { if (openRoot) store.openConversation({ id: 'root', cwd: '/home/u/work/mission', model: 'opus', access: 'full' }, { expand: false }) }, []) // eslint-disable-line react-hooks/exhaustive-deps
  return <CoderBubbles store={store} onNavigate={navigate} />
}

const S = (id, parent, extra = {}) => ({ id, parent_session_id: parent, conversation_id: `conv-${id}`, title: `Agent ${id}`, state: 'running', counts: {}, ...extra })
// 1 mission lead (root) -> 2 direction leads -> 4 sub-agent (of 2) -> 5 (of 4) -> 6 (of 5)
const TREE = [S(1, 0, { conversation_id: 'root', title: 'Mission' }), S(2, 1, { title: 'Direction A' }), S(3, 1, { title: 'Direction B', state: 'draft' }),
  S(4, 2, { title: 'Worker A1' }), S(5, 4, { title: 'Worker A1a' }), S(6, 5, { title: 'Too deep' })]

beforeEach(() => {
  vi.clearAllMocks()
  Element.prototype.scrollTo = function scrollTo() {}
  listeners.chat.clear()
  localStorage.clear()
  api.getChatTurns.mockResolvedValue({ items: [] })
  api.getChatEvents.mockResolvedValue({ items: [], hasMore: false })
  api.coderStatus.mockResolvedValue({ enabled: true, ready: true, defaultAccess: 'full', workspaceRoot: '/home/u/monoagent-coder' })
  api.coderWorkspaceList.mockResolvedValue([])
  api.getAgentRuntimeModels.mockResolvedValue([])
  api.getProfiles.mockResolvedValue([])
  api.stopChatTurn.mockResolvedValue({ ok: true })
  chatAccess.record.mockResolvedValue({ access: 'full', mode: 'coder' })
  sessions.list.mockResolvedValue({ sessions: TREE })
  sessions.show.mockResolvedValue({}); sessions.board.mockResolvedValue({}); sessions.pulse.mockResolvedValue({})
  sessions.pause.mockResolvedValue({}); sessions.resume.mockResolvedValue({}); sessions.stop.mockResolvedValue({})
  mockConfirm.mockResolvedValue(true)
})
afterEach(cleanup)

const orbit = () => screen.findByRole('group', { name: /Agents of/ })

describe('the mission orbit in the dock', () => {
  it('draws the direction leads around the root bubble, and no deeper agents', async () => {
    render(<Harness />)
    expect(await screen.findByRole('button', { name: 'Direction A, Running, 1 agent of its own' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Direction B, Waiting for you' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Worker A1/ })).toBeNull()
    expect(within(await orbit()).getAllByRole('button')).toHaveLength(2)
  })

  it('draws no orbit around a chat that leads no session', async () => {
    sessions.list.mockResolvedValue({ sessions: [S(1, 0, { conversation_id: 'other' })] })
    render(<Harness />)
    await waitFor(() => expect(sessions.list).toHaveBeenCalled())
    expect(screen.queryByTestId('orbit')).toBeNull()
  })

  it('keeps the dock when the sessions cannot be read', async () => {
    sessions.list.mockResolvedValue({ error: 'nope' })
    render(<Harness />)
    await waitFor(() => expect(sessions.list).toHaveBeenCalled())
    expect(screen.queryByTestId('orbit')).toBeNull()
    expect(document.querySelector('[data-bubble="root"]')).toBeInTheDocument()
  })

  it('opens a direction as a simple pane, with no bubble of its own in the dock', async () => {
    render(<Harness />)
    fireEvent.click(await screen.findByRole('button', { name: /^Direction A/ }))
    const pane = await screen.findByTestId('bubble-overlay')
    expect(within(pane).getByRole('heading', { name: 'Direction A' })).toBeInTheDocument()
    expect(document.querySelector('[data-bubble="conv-2"]')).toBeNull()
    expect(store.bubbles.find(b => b.key === 'conv-2')).toMatchObject({ lite: true, parentKey: 'root', sessionId: 2 })
  })
})

describe('the simple pane', () => {
  const openA = async () => {
    render(<Harness />)
    fireEvent.click(await screen.findByRole('button', { name: /^Direction A/ }))
    return screen.findByTestId('bubble-overlay')
  }

  it('has no configuration: no settings, org stage, access chip, clear or compact, budget or autonomy', async () => {
    const pane = await openA()
    await within(pane).findByTestId('lite-head')
    expect(within(pane).queryByRole('button', { name: 'Chat settings' })).toBeNull()
    expect(pane.querySelector('[aria-haspopup="dialog"]')).toBeNull()
    for (const testid of ['chip-access', 'chip-confined', 'bubble-stage']) expect(within(pane).queryByTestId(testid)).toBeNull()
    expect(within(pane).queryByRole('button', { name: /^(Clear|Compact)$/ })).toBeNull()
    expect(within(pane).queryByRole('separator')).toBeNull()
    expect(pane.textContent).not.toMatch(/autonomy|budget|access level/i)
  })

  it('shows the title and state, a task board link, pause and stop, and a message box', async () => {
    const pane = await openA()
    expect(pane.querySelector('.lite-state')).toHaveTextContent('Running')
    expect(within(pane).getByRole('button', { name: 'Task board' })).toBeInTheDocument()
    expect(within(pane).getByRole('button', { name: 'Pause' })).toBeInTheDocument()
    expect(within(pane).getByRole('button', { name: 'Stop' })).toBeInTheDocument()
    expect(within(pane).getByRole('textbox')).toBeInTheDocument()
  })

  it('goes to the task board of the agent', async () => {
    const pane = await openA()
    fireEvent.click(within(pane).getByRole('button', { name: 'Task board' }))
    expect(navigate).toHaveBeenCalledWith('tasks', { sessionId: 2 })
  })

  it('pauses and stops through the task session; stop asks first', async () => {
    const pane = await openA()
    fireEvent.click(within(pane).getByRole('button', { name: 'Pause' }))
    await waitFor(() => expect(sessions.pause).toHaveBeenCalledWith(2))
    mockConfirm.mockResolvedValueOnce(false)
    fireEvent.click(within(pane).getByRole('button', { name: 'Stop' }))
    await waitFor(() => expect(mockConfirm).toHaveBeenCalled())
    expect(sessions.stop).not.toHaveBeenCalled()
    fireEvent.click(within(pane).getByRole('button', { name: 'Stop' }))
    await waitFor(() => expect(sessions.stop).toHaveBeenCalledWith(2))
  })

  it('shows its own agents one orbit deeper, and opens them the same way', async () => {
    const pane = await openA()
    const w = await within(pane).findByRole('button', { name: /^Worker A1, Running, 1 agent of its own/ })
    expect(within(pane).queryByRole('button', { name: /Direction/ })).toBeNull()
    fireEvent.click(w)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Worker A1' })).toBeInTheDocument())
    expect(screen.getByRole('button', { name: /^Worker A1a/ })).toBeInTheDocument()
    expect(store.bubbles.find(b => b.key === 'conv-4')).toMatchObject({ lite: true, parentKey: 'conv-2' })
  })

  it('stops listing agents below the fourth level', async () => {
    const pane = await openA()
    fireEvent.click(await within(pane).findByRole('button', { name: /^Worker A1,/ }))
    fireEvent.click(await screen.findByRole('button', { name: /^Worker A1a/ }))
    await screen.findByRole('heading', { name: 'Worker A1a' })
    expect(screen.queryByRole('button', { name: /Too deep/ })).toBeNull()
  })

  it('Escape closes the pane and goes back one level at a time', async () => {
    const pane = await openA()
    fireEvent.click(await within(pane).findByRole('button', { name: /^Worker A1,/ }))
    await screen.findByRole('heading', { name: 'Worker A1' })
    fireEvent.keyDown(document.body, { key: 'Escape' })
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Direction A' })).toBeInTheDocument())
    expect(store.bubbles.some(b => b.key === 'conv-4')).toBe(false)
    // Out of the last agent, back at the root pane; one more Escape shrinks that into its bubble.
    fireEvent.keyDown(document.body, { key: 'Escape' })
    await waitFor(() => expect(screen.getByRole('dialog', { name: /mission/ })).toBeInTheDocument())
    expect(store.bubbles.map(b => b.key)).toEqual(['root'])
    fireEvent.keyDown(document.body, { key: 'Escape' })
    await waitFor(() => expect(screen.queryByTestId('bubble-overlay')).toBeNull())
  })

  it('is not kept across a restart', async () => {
    await openA()
    await waitFor(() => expect(localStorage.length).toBeGreaterThan(0))
    expect(Object.values(localStorage).join('')).not.toContain('conv-2')
  })

  it('a direction without a chat opens its task board instead', async () => {
    sessions.list.mockResolvedValue({ sessions: [S(1, 0, { conversation_id: 'root' }), S(2, 1, { conversation_id: '', title: 'No chat yet' })] })
    render(<Harness />)
    fireEvent.click(await screen.findByRole('button', { name: /^No chat yet/ }))
    expect(navigate).toHaveBeenCalledWith('tasks', { sessionId: 2 })
    expect(screen.queryByTestId('bubble-overlay')).toBeNull()
  })

  it('renders a tool call as a collapsed one-line chip that expands, and text as text', async () => {
    api.getChatTurns.mockImplementation(async id => (id === 'conv-2' ? { items: [{ id: 't1', status: 'completed' }] } : { items: [] }))
    api.getChatEvents.mockImplementation(async id => (id === 'conv-2' ? { hasMore: false, items: [
      { seq: 1, turnId: 't1', type: 'turn.started', at: '2026-10-28T10:00:00Z', payload: {} },
      { seq: 2, turnId: 't1', type: 'assistant.delta', payload: { text: 'Looking <img src=x onerror=alert(1)> now' } },
      { seq: 3, turnId: 't1', type: 'tool.started', at: '2026-10-28T10:00:01Z', payload: { callId: 'k1', name: 'Bash', arguments: { command: 'go test ./...' }, native: true } },
      { seq: 4, turnId: 't1', type: 'tool.completed', at: '2026-10-28T10:00:02Z', payload: { callId: 'k1', result: 'ok  all packages', ok: true } },
      { seq: 5, turnId: 't1', type: 'turn.finished', payload: { status: 'completed' } },
    ] } : { items: [], hasMore: false }))
    const pane = await openA()
    const chip = await within(pane).findByRole('button', { name: /Bash · go test/ })
    expect(chip).toHaveAttribute('aria-expanded', 'false')
    expect(pane.textContent).toContain('<img src=x onerror=alert(1)>')
    expect(pane.querySelector('img')).toBeNull()
    expect(pane.textContent).not.toContain('ok  all packages')
    fireEvent.click(chip)
    expect(chip).toHaveAttribute('aria-expanded', 'true')
    expect(pane.textContent).toContain('ok  all packages')
  })
})
