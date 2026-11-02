// @vitest-environment jsdom
import React, { useEffect } from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, renderHook, screen, fireEvent, waitFor, cleanup, act, within } from '@testing-library/react'
import '../../i18n.js'

const { api, chatAccess, listeners, mockConfirm } = vi.hoisted(() => ({
  listeners: { chat: new Set() },
  chatAccess: { create: vi.fn(), setAccess: vi.fn(), record: vi.fn() },
  mockConfirm: vi.fn(),
  api: {
    getChatMissionToggle: vi.fn(() => Promise.resolve({})), // the Mission switch in the pane header
    setChatMissionToggle: vi.fn(),
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
vi.mock('../ConfirmDialog.jsx', () => ({ confirm: (...a) => mockConfirm(...a) }))

import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import CoderBubbles from './CoderBubbles.jsx'
import { useCoderBubbles } from './useCoderBubbles.js'
import { useCoderConversation } from './useCoderConversation.js'

const emit = ev => act(() => { for (const cb of [...listeners.chat]) cb(ev) })

let store
function Harness({ open }) {
  store = useCoderBubbles()
  useEffect(() => { if (open) store.openConversation(open) }, []) // eslint-disable-line react-hooks/exhaustive-deps
  return <CoderBubbles store={store} onNavigate={() => {}} />
}

const conv = { id: 'c1', cwd: '/home/u/monoagent-coder/20260929-brisk-otter', model: 'opus' }

beforeEach(() => {
  vi.clearAllMocks()
  // jsdom has no scrolling; the transcript follows new output with it.
  Element.prototype.scrollTo = function scrollTo() {}
  listeners.chat.clear()
  localStorage.clear()
  api.getChatTurns.mockResolvedValue({ items: [] })
  api.getChatEvents.mockResolvedValue({ items: [], hasMore: false })
  api.coderStatus.mockResolvedValue({ enabled: true, ready: true, defaultAccess: 'full', workspaceRoot: '/home/u/monoagent-coder' })
  api.coderWorkspaceList.mockResolvedValue([])
  api.getAgentRuntimeModels.mockResolvedValue([{ id: 'opus', label: 'Opus 5.5', effort_levels: ['low', 'high'] }])
  api.startChatTurn.mockResolvedValue({ ok: true })
  api.stopChatTurn.mockResolvedValue({ ok: true })
  api.getProfiles.mockResolvedValue([])
  chatAccess.record.mockResolvedValue({ access: 'full', mode: 'coder' })
})
afterEach(cleanup)

const overlay = () => screen.queryByTestId('bubble-overlay')
const backdrop = () => screen.getByTestId('bubble-backdrop')
const bubble = key => document.querySelector(`[data-bubble="${key}"]`)

describe('coder bubbles', () => {
  it('opens a conversation expanded, and a backdrop click collapses it back into its bubble', async () => {
    render(<Harness open={conv} />)
    await waitFor(() => expect(overlay()).toBeInTheDocument())
    expect(bubble('c1')).toHaveTextContent('BO')
    fireEvent.pointerDown(backdrop())
    fireEvent.click(backdrop())
    await waitFor(() => expect(overlay()).toBeNull())
    expect(bubble('c1')).toBeInTheDocument()
    expect(api.stopChatTurn).not.toHaveBeenCalled()
  })

  it("names a chat in a profile's own folder by the profile, not by its id (#477)", async () => {
    const root = '/home/u/.monoagent/profiles/ab12cd34-77b2-4c55-9d1a-6e2f4b8c1d20'
    api.getProfiles.mockResolvedValue([{ id: 'ab12cd34-77b2-4c55-9d1a-6e2f4b8c1d20', name: 'Work', root_dir: root }])
    render(<Harness open={{ ...conv, cwd: root }} />)
    await waitFor(() => expect(overlay()).toBeInTheDocument())
    await waitFor(() => expect(bubble('c1')).toHaveTextContent('WO'))
    expect(bubble('c1')).toHaveAttribute('aria-label', expect.stringContaining('Work'))
    // The title says Work; the full folder is still shown where the chat works.
    expect(within(overlay()).getByText('Work')).toBeInTheDocument()
    expect(within(overlay()).queryByText('ab12cd34-77b2-4c55-9d1a-6e2f4b8c1d20')).toBeNull()
  })

  it('does not collapse for a drag that started inside, a text selection, or an open dialog', async () => {
    render(<Harness open={conv} />)
    await waitFor(() => expect(overlay()).toBeInTheDocument())

    // A press inside the chat that ends on the backdrop (drag / select).
    fireEvent.pointerDown(overlay())
    fireEvent.click(backdrop())
    expect(overlay()).toBeInTheDocument()

    // Text selected somewhere.
    const sel = vi.spyOn(window, 'getSelection').mockReturnValue({ toString: () => 'some text' })
    fireEvent.pointerDown(backdrop())
    fireEvent.click(backdrop())
    expect(overlay()).toBeInTheDocument()
    sel.mockRestore()

    // A modal dialog on top.
    const modal = document.createElement('div')
    modal.setAttribute('aria-modal', 'true')
    document.body.appendChild(modal)
    fireEvent.pointerDown(backdrop())
    fireEvent.click(backdrop())
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(overlay()).toBeInTheDocument()
    modal.remove()

    fireEvent.keyDown(window, { key: 'Escape' })
    await waitFor(() => expect(overlay()).toBeNull())
  })

  it('clicking the bubble toggles, and another bubble switches without closing', async () => {
    render(<Harness open={conv} />)
    await waitFor(() => expect(overlay()).toBeInTheDocument())
    act(() => { store.openConversation({ id: 'c2', cwd: '/w/mono-agent', model: 'sonnet' }) })
    await waitFor(() => expect(screen.getByTestId('bubble-overlay')).toHaveAttribute('aria-label', 'Coder chat: mono-agent'))
    fireEvent.click(bubble('c1'))
    await waitFor(() => expect(screen.getByTestId('bubble-overlay')).toHaveAttribute('aria-label', 'Coder chat: 20260929-brisk-otter'))
    fireEvent.click(bubble('c1'))
    await waitFor(() => expect(overlay()).toBeNull())
  })

  it('re-attaches to a running turn, keeps it running when collapsed, and counts the finish as unread', async () => {
    api.getChatTurns.mockResolvedValue({ items: [{ id: 't1', prompt: 'fix the build', status: 'active', ownedByThisInstance: true }] })
    render(<Harness open={conv} />)
    await waitFor(() => expect(screen.getByText('fix the build')).toBeInTheDocument())
    await waitFor(() => expect(document.querySelector('[data-testid="stage-node"][data-agent="lead"]')).toHaveAttribute('data-status', 'working'))
    expect(bubble('c1')).toHaveAttribute('data-status', 'working')

    fireEvent.keyDown(window, { key: 'Escape' })
    await waitFor(() => expect(overlay()).toBeNull())
    expect(api.stopChatTurn).not.toHaveBeenCalled()

    emit({ conversationId: 'c1', turnId: 't1', seq: 5, type: 'turn.finished', payload: { status: 'completed' } })
    expect(bubble('c1')).toHaveAttribute('data-status', 'done')
    expect(bubble('c1').querySelector('.bubble-badge')).toHaveTextContent('1')
    fireEvent.click(bubble('c1'))
    await waitFor(() => expect(overlay()).toBeInTheDocument())
    expect(bubble('c1').querySelector('.bubble-badge')).toBeNull()
  })

  it('asks before closing a working chat, and stops its turn', async () => {
    api.getChatTurns.mockResolvedValue({ items: [{ id: 't1', prompt: 'go', status: 'active' }] })
    render(<Harness open={conv} />)
    await waitFor(() => expect(bubble('c1')).toHaveAttribute('data-status', 'working'))

    mockConfirm.mockResolvedValueOnce(false)
    fireEvent.click(screen.getByLabelText('Close 20260929-brisk-otter'))
    await waitFor(() => expect(mockConfirm).toHaveBeenCalledTimes(1))
    expect(bubble('c1')).toBeInTheDocument()
    expect(api.stopChatTurn).not.toHaveBeenCalled()

    mockConfirm.mockResolvedValueOnce(true)
    fireEvent.click(screen.getByLabelText('Close 20260929-brisk-otter'))
    await waitFor(() => expect(bubble('c1')).toBeNull())
    expect(api.stopChatTurn).toHaveBeenCalledWith('c1', 't1')
  })

  it('closes an idle chat without asking', async () => {
    render(<Harness open={conv} />)
    await waitFor(() => expect(bubble('c1')).toBeInTheDocument())
    fireEvent.click(screen.getByLabelText('Close 20260929-brisk-otter'))
    await waitFor(() => expect(bubble('c1')).toBeNull())
    expect(mockConfirm).not.toHaveBeenCalled()
  })

  it('starts a new coder chat from +, creates the conversation on the first message and keeps the bubble', async () => {
    api.coderWorkspaceDefault.mockResolvedValue({ path: '/home/u/monoagent-coder', created: false })
    chatAccess.create.mockResolvedValue({ id: 'c9', cwd: '/home/u/monoagent-coder', model: 'opus', access: 'full' })
    render(<Harness />)
    fireEvent.click(await screen.findByLabelText('New chat'))
    await waitFor(() => expect(screen.getByTestId('chat-setup')).toBeInTheDocument())
    await waitFor(() => expect(screen.getByLabelText('Model')).toHaveValue('opus'))
    const box = within(overlay()).getByPlaceholderText('Type a message...')
    fireEvent.change(box, { target: { value: 'add a README' } })
    fireEvent.change(screen.getByLabelText('Effort'), { target: { value: 'high' } })
    fireEvent.keyDown(box, { key: 'Enter' })
    await waitFor(() => expect(chatAccess.create).toHaveBeenCalledWith('general', 'claude', 'opus', 'high', 'full', '/home/u/monoagent-coder'))
    expect(api.coderWorkspaceDefault).toHaveBeenCalledWith('claude')
    await waitFor(() => expect(api.startChatTurn).toHaveBeenCalledWith('c9', expect.any(String), 'add a README', false, false))
    await waitFor(() => expect(store.bubbles[0].conversationId).toBe('c9'))
    expect(JSON.parse(localStorage.getItem('monoagent:coderBubbles:v1')).bubbles[0].conversationId).toBe('c9')
  })

  it('does not send the first message with the Mission toggle off when applying the choice failed, and keeps the choice and the chat for the retry', async () => {
    api.coderWorkspaceDefault.mockResolvedValue({ path: '/home/u/monoagent-coder', created: false })
    chatAccess.create.mockResolvedValue({ id: 'c9', cwd: '/home/u/monoagent-coder', model: 'opus', access: 'full' })
    api.setChatMissionToggle.mockRejectedValueOnce(new Error('database is locked')).mockResolvedValue({ conversationId: 'c9', mission: { on: true, locked: false, available: true } })
    render(<Harness />)
    fireEvent.click(await screen.findByLabelText('New chat'))
    await waitFor(() => expect(screen.getByTestId('chat-setup')).toBeInTheDocument())
    await waitFor(() => expect(screen.getByLabelText('Model')).toHaveValue('opus'))
    fireEvent.click(await screen.findByRole('switch', { name: /mission/i }))
    const box = within(overlay()).getByPlaceholderText('Type a message...')
    fireEvent.change(box, { target: { value: 'add a README' } })
    fireEvent.keyDown(box, { key: 'Enter' })
    await waitFor(() => expect(api.setChatMissionToggle).toHaveBeenCalledWith('c9', true))
    expect((await screen.findAllByText(/database is locked/)).length).toBeGreaterThan(0)
    expect(api.startChatTurn).not.toHaveBeenCalled() // no first message with the toggle off
    expect(store.bubbles[0].conversationId).toBeFalsy()
    expect(screen.getByRole('switch', { name: /mission/i })).toHaveAttribute('aria-checked', 'true') // the choice stays
    fireEvent.change(box, { target: { value: 'add a README' } })
    fireEvent.keyDown(box, { key: 'Enter' })
    await waitFor(() => expect(api.startChatTurn).toHaveBeenCalledWith('c9', expect.any(String), 'add a README', false, false))
    expect(chatAccess.create).toHaveBeenCalledTimes(1) // the retry reused the chat it had made
    expect(api.setChatMissionToggle).toHaveBeenCalledTimes(2)
    expect(api.setChatMissionToggle).toHaveBeenLastCalledWith('c9', true)
  })

  it('offers the ready coder runtimes and creates the chat on the one picked, with its model and effort', async () => {
    api.coderStatus.mockResolvedValue({
      enabled: true, ready: true, defaultAccess: 'full',
      runtimes: [
        { id: 'claude', ready: true, effort: true, toolActivity: 'full' },
        { id: 'codex', ready: true, effort: true, toolActivity: 'commands' },
        { id: 'gemini', ready: false, installed: true, fullAccess: false },
      ],
    })
    api.getAgentRuntimeModels.mockImplementation(rt => Promise.resolve(rt === 'codex'
      ? [{ id: 'gpt-5', label: 'GPT-5', effort_levels: ['low', 'medium'] }]
      : [{ id: 'opus', label: 'Opus 5.5', effort_levels: ['low', 'high'] }]))
    api.coderWorkspaceDefault.mockResolvedValue({ path: '/home/u/monoagent-coder', created: true })
    chatAccess.create.mockResolvedValue({ id: 'c9', cwd: '/home/u/monoagent-coder', model: 'gpt-5', runtimeId: 'codex', access: 'full' })
    render(<Harness />)
    fireEvent.click(await screen.findByLabelText('New chat'))
    const runtime = await screen.findByLabelText('Runtime')
    await waitFor(() => expect(runtime).toHaveValue('claude'))
    expect([...runtime.querySelectorAll('option')].map(o => o.value)).toEqual(['claude', 'codex'])

    fireEvent.change(runtime, { target: { value: 'codex' } })
    await waitFor(() => expect(screen.getByLabelText('Model')).toHaveValue('gpt-5'))
    expect(api.getAgentRuntimeModels).toHaveBeenCalledWith('codex', '')
    expect(screen.getByText('codex shows commands, not every result')).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('Effort'), { target: { value: 'medium' } })

    const box = within(overlay()).getByPlaceholderText('Type a message...')
    fireEvent.change(box, { target: { value: 'port it' } })
    fireEvent.keyDown(box, { key: 'Enter' })
    await waitFor(() => expect(chatAccess.create).toHaveBeenCalledWith('general', 'codex', 'gpt-5', 'medium', 'full', '/home/u/monoagent-coder'))
    expect(api.coderWorkspaceDefault).toHaveBeenCalledWith('codex')
    await waitFor(() => expect(store.bubbles[0].runtime).toBe('codex'))
  })

  it('asks before closing a new chat that has unsent text', async () => {
    render(<Harness />)
    fireEvent.click(await screen.findByLabelText('New chat'))
    await waitFor(() => expect(screen.getByLabelText('Model')).toHaveValue('opus'))
    fireEvent.change(within(overlay()).getByPlaceholderText('Type a message...'), { target: { value: 'a long plan' } })
    fireEvent.keyDown(window, { key: 'Escape' })
    await waitFor(() => expect(overlay()).toBeNull())
    const key = store.bubbles[0].key

    mockConfirm.mockResolvedValueOnce(false)
    fireEvent.click(screen.getByLabelText('Close New chat'))
    await waitFor(() => expect(mockConfirm).toHaveBeenCalledTimes(1))
    expect(bubble(key)).toBeInTheDocument()

    mockConfirm.mockResolvedValueOnce(true)
    fireEvent.click(screen.getByLabelText('Close New chat'))
    await waitFor(() => expect(bubble(key)).toBeNull())
  })

  it('keeps the unsent draft across a collapse', async () => {
    render(<Harness open={conv} />)
    await waitFor(() => expect(overlay()).toBeInTheDocument())
    await waitFor(() => expect(within(overlay()).getByPlaceholderText('Type a message...')).not.toBeDisabled())
    fireEvent.change(within(overlay()).getByPlaceholderText('Type a message...'), { target: { value: 'half a thought' } })
    fireEvent.keyDown(window, { key: 'Escape' })
    await waitFor(() => expect(overlay()).toBeNull())
    fireEvent.click(bubble('c1'))
    await waitFor(() => expect(within(overlay()).getByPlaceholderText('Type a message...')).toHaveValue('half a thought'))
  })

  it('still offers + when coder mode is off: a scoped chat needs no coder', async () => {
    api.coderStatus.mockResolvedValue({ enabled: false })
    render(<Harness />)
    expect(await screen.findByLabelText('New chat')).toBeInTheDocument()
  })

  it('restores bubbles from a previous session', async () => {
    localStorage.setItem('monoagent:coderBubbles:v1', JSON.stringify({ side: 'left', bubbles: [{ conversationId: 'c1', cwd: '/w/api', model: 'opus' }] }))
    api.getChatTurns.mockResolvedValue({ items: [{ id: 't1', status: 'failed' }] })
    render(<Harness />)
    await waitFor(() => expect(bubble('c1')).toHaveAttribute('data-status', 'error'))
    expect(screen.getByTestId('bubble-dock')).toHaveClass('left')
    expect(overlay()).toBeNull()
  })
})

describe('restored bubbles', () => {
  it('drops a bubble whose conversation was deleted', async () => {
    localStorage.setItem('monoagent:coderBubbles:v1', JSON.stringify({ bubbles: [
      { conversationId: 'gone', cwd: '/w/old', model: 'opus' },
      { conversationId: 'c1', cwd: '/w/api', model: 'opus' },
    ] }))
    api.getChatTurns.mockImplementation(id => Promise.resolve(id === 'gone' ? { items: [], nextCursor: '', notFound: true } : { items: [] }))
    render(<Harness />)
    await waitFor(() => expect(bubble('gone')).toBeNull())
    expect(bubble('c1')).toBeInTheDocument()
    // Saved by an effect after the render that dropped it.
    await waitFor(() => expect(JSON.parse(localStorage.getItem('monoagent:coderBubbles:v1')).bubbles.map(b => b.conversationId)).toEqual(['c1']))
  })
})

describe('useCoderConversation', () => {
  it('sends once when Enter is pressed twice while the conversation is being created', async () => {
    let finish
    const create = vi.fn(() => new Promise(res => { finish = res }))
    const { result } = renderHook(() => useCoderConversation({ conversationId: '', create }))
    let first, second
    act(() => {
      first = result.current.send('hello')
      second = result.current.send('hello')
    })
    await expect(second).resolves.toBeNull()
    await act(async () => { finish({ id: 'c9', cwd: '/w' }); await first })
    expect(await first).toBe(true)
    expect(create).toHaveBeenCalledTimes(1)
    expect(api.startChatTurn).toHaveBeenCalledTimes(1)
    expect(result.current.messages.filter(m => m.role === 'user')).toHaveLength(1)
  })

  it('reports a refused start in the transcript', async () => {
    api.startChatTurn.mockResolvedValue({ ok: false, status: 'busy' })
    const { result } = renderHook(() => useCoderConversation({ conversationId: '', create: () => Promise.resolve({ id: 'c9' }) }))
    let ok
    await act(async () => { ok = await result.current.send('hi') })
    expect(ok).toBe(false)
    expect(result.current.messages.at(-1)).toEqual({ role: 'error', content: 'Could not start: busy' })
  })
})

describe('empty new chats', () => {
  it('discards a new chat collapsed with nothing typed, and keeps one with a draft', async () => {
    render(<Harness />)
    fireEvent.click(await screen.findByLabelText('New chat'))
    await waitFor(() => expect(overlay()).toBeInTheDocument())
    fireEvent.keyDown(window, { key: 'Escape' })
    await waitFor(() => expect(overlay()).toBeNull())
    expect(store.bubbles).toHaveLength(0)

    fireEvent.click(screen.getByLabelText('New chat'))
    await waitFor(() => expect(screen.getByLabelText('Model')).toHaveValue('opus'))
    fireEvent.change(within(overlay()).getByPlaceholderText('Type a message...'), { target: { value: 'later' } })
    fireEvent.keyDown(window, { key: 'Escape' })
    await waitFor(() => expect(overlay()).toBeNull())
    expect(store.bubbles).toHaveLength(1)
  })
})

describe('bubble cost (#230)', () => {
  it('marks a cost with a worker\'s estimate in it with "≈"', async () => {
    render(<Harness open={conv} />)
    await waitFor(() => expect(overlay()).toBeInTheDocument())
    act(() => { store.openConversation({ id: 'c2', cwd: '/w/api', model: 'sonnet' }) })
    await waitFor(() => expect(bubble('c2')).toHaveAttribute('aria-pressed', 'true'))
    emit({ conversationId: 'c1', turnId: 't1', seq: 1, type: 'usage.updated', payload: { costUsd: 0.1 } })
    emit({ conversationId: 'c1', turnId: 't1', seq: 2, type: 'usage.updated', payload: { agentId: 'w1', costUsd: 0.35, costEstimated: true } })
    fireEvent.mouseEnter(bubble('c1').parentElement)
    const cost = screen.getByTitle('Partly estimated from the tokens used: some runtimes report no cost')
    expect(cost).toHaveTextContent('≈$0.4500')
  })
})

// #231 phases 2 and 3: the verification matrix for coder bubbles.
describe('verification matrix (#231)', () => {
  const at = n => `2026-09-30T10:00:0${n}.000Z`

  it('keeps three concurrent chats apart: each bubble follows only its own turn', async () => {
    render(<Harness open={conv} />)
    await waitFor(() => expect(overlay()).toBeInTheDocument())
    act(() => { store.openConversation({ id: 'c2', cwd: '/w/api', model: 'sonnet' }) })
    act(() => { store.openConversation({ id: 'c3', cwd: '/w/web', model: 'opus' }) })
    await waitFor(() => expect(bubble('c3')).toHaveAttribute('aria-pressed', 'true'))

    for (const [id, turn] of [['c1', 't1'], ['c2', 't2'], ['c3', 't3']]) {
      emit({ conversationId: id, turnId: turn, seq: 1, at: at(1), type: 'turn.started', payload: {} })
    }
    for (const id of ['c1', 'c2', 'c3']) expect(bubble(id)).toHaveAttribute('data-status', 'working')

    emit({ conversationId: 'c2', turnId: 't2', seq: 2, type: 'usage.updated', payload: { costUsd: 0.25 } })
    emit({ conversationId: 'c1', turnId: 't1', seq: 2, type: 'turn.finished', payload: { status: 'completed' } })
    emit({ conversationId: 'c2', turnId: 't2', seq: 3, type: 'turn.finished', payload: { status: 'failed' } })
    // An event for a chat that has no bubble changes nothing.
    emit({ conversationId: 'other', turnId: 'x', seq: 1, type: 'turn.started', payload: {} })

    expect(bubble('c1')).toHaveAttribute('data-status', 'done')
    expect(bubble('c2')).toHaveAttribute('data-status', 'error')
    expect(bubble('c3')).toHaveAttribute('data-status', 'working')
    expect(bubble('c1').querySelector('.bubble-badge')).toHaveTextContent('1')
    expect(bubble('c2').querySelector('.bubble-badge')).toHaveTextContent('1')
    expect(bubble('c3').querySelector('.bubble-badge')).toBeNull()
    expect(store.summaryOf('c2').costByTurn).toEqual({ t2: 0.25 })
    expect(store.summaryOf('c1').costByTurn).toEqual({})
    expect(document.querySelectorAll('[data-bubble]')).toHaveLength(3)

    emit({ conversationId: 'c3', turnId: 't3', seq: 2, type: 'turn.finished', payload: { status: 'completed' } })
    expect(bubble('c3')).toHaveAttribute('data-status', 'done')
    expect(bubble('c3').querySelector('.bubble-badge')).toBeNull() // it was open: read
  })

  it('does not collapse for a click inside the overlay or in a portal it owns; the collapse button does, without stopping work', async () => {
    api.getChatTurns.mockResolvedValue({ items: [{ id: 't1', prompt: 'go', status: 'active' }] })
    render(<Harness open={conv} />)
    await waitFor(() => expect(document.querySelector('[data-testid="stage-node"][data-agent="lead"]')).toHaveAttribute('data-status', 'working'))

    fireEvent.pointerDown(overlay())
    fireEvent.click(overlay())
    expect(overlay()).toBeInTheDocument()

    // A menu or tooltip portaled to <body> (outside the backdrop).
    const menu = document.createElement('div')
    menu.setAttribute('role', 'menu')
    document.body.appendChild(menu)
    fireEvent.pointerDown(menu)
    fireEvent.click(menu)
    expect(overlay()).toBeInTheDocument()
    menu.remove()

    fireEvent.click(within(overlay()).getByLabelText('Collapse (keeps it running)'))
    await waitFor(() => expect(overlay()).toBeNull())
    expect(bubble('c1')).toHaveAttribute('data-status', 'working')
    expect(api.stopChatTurn).not.toHaveBeenCalled()
  })

  it('goes full-screen with a collapse button in a narrow window', async () => {
    const width = window.innerWidth
    window.innerWidth = 500
    try {
      render(<Harness open={conv} />)
      await waitFor(() => expect(overlay()).toBeInTheDocument())
      expect(overlay()).toHaveClass('narrow')
      fireEvent.click(within(overlay()).getByLabelText('Collapse (keeps it running)'))
      await waitFor(() => expect(overlay()).toBeNull())
    } finally {
      window.innerWidth = width
    }
  })

  it('replays a finished turn from the journal exactly as it showed live', async () => {
    let journal = null // events getChatEvents serves once the turn is stored
    api.getChatEvents.mockImplementation((_c, _t, afterSeq) => Promise.resolve(
      journal ? { items: journal.filter(e => e.seq > afterSeq), hasMore: false } : { items: [], hasMore: false }))
    render(<Harness open={conv} />)
    await waitFor(() => expect(overlay()).toBeInTheDocument())
    const box = within(overlay()).getByPlaceholderText('Type a message...')
    fireEvent.change(box, { target: { value: 'run the tests' } })
    fireEvent.keyDown(box, { key: 'Enter' })
    await waitFor(() => expect(api.startChatTurn).toHaveBeenCalled())
    const turnId = api.startChatTurn.mock.calls[0][1]
    const events = [
      { type: 'turn.started', payload: {} },
      { type: 'assistant.delta', payload: { partId: 'p1', text: 'Running the ' } },
      { type: 'assistant.delta', payload: { partId: 'p1', text: 'tests now.' } },
      { type: 'tool.started', payload: { callId: 'k1', name: 'Bash', native: true, kind: 'shell', arguments: { command: 'make test' } } },
      { type: 'tool.completed', payload: { callId: 'k1', ok: false, result: 'FAIL: TestX' } },
      { type: 'assistant.delta', payload: { partId: 'p2', text: 'One test fails: TestX.' } },
      { type: 'usage.updated', payload: { inputTokens: 120, outputTokens: 30, costUsd: 0.0123, source: 'runtime' } },
      { type: 'turn.finished', payload: { status: 'completed' } },
    ].map((e, i) => ({ ...e, conversationId: 'c1', turnId, seq: i + 1, at: at(i) }))
    for (const ev of events) emit(ev)
    await waitFor(() => expect(within(overlay()).getByText('One test fails: TestX.')).toBeInTheDocument())
    // The settled state, not "anything but working": before the stage has folded
    // the events in, the lead is still idle, and a snapshot of that differs from
    // the replay's done.
    await waitFor(() => expect(document.querySelector('[data-testid="stage-node"][data-agent="lead"]')).toHaveAttribute('data-status', 'done'))
    // Stable fields only: the stage has a clock-driven ticker, so compare the
    // transcript and the lead's status, not the overlay's raw text.
    const snapshot = () => ({
      transcript: screen.getByTestId('bubble-transcript').textContent,
      lead: document.querySelector('[data-testid="stage-node"][data-agent="lead"]')?.getAttribute('data-status'),
    })
    const live = snapshot()

    fireEvent.keyDown(window, { key: 'Escape' })
    await waitFor(() => expect(overlay()).toBeNull())
    journal = events
    api.getChatTurns.mockResolvedValue({ items: [{ id: turnId, prompt: 'run the tests', status: 'completed' }] })
    fireEvent.click(bubble('c1'))
    await waitFor(() => expect(within(overlay()).getByText('One test fails: TestX.')).toBeInTheDocument())
    await waitFor(() => expect(snapshot()).toEqual(live))
  })

  it('collapses at once with reduced motion, and animates otherwise', async () => {
    render(<Harness open={conv} />)
    await waitFor(() => expect(overlay()).toBeInTheDocument())
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(overlay()).toHaveClass('closing') // the shrink animation plays first
    await waitFor(() => expect(overlay()).toBeNull())

    const had = window.matchMedia
    window.matchMedia = q => ({ matches: q.includes('prefers-reduced-motion'), media: q, addEventListener() {}, removeEventListener() {} })
    try {
      fireEvent.click(bubble('c1'))
      await waitFor(() => expect(overlay()).toBeInTheDocument())
      fireEvent.keyDown(window, { key: 'Escape' })
      expect(overlay()).toBeNull()
    } finally {
      window.matchMedia = had
    }
  })

  it('switches every bubble animation off under prefers-reduced-motion', () => {
    const css = readFileSync(join(__dirname, 'bubbles.css'), 'utf8')
    // Every rule inside the reduced-motion block(s) that switches animation
    // and transition off, whatever its selector list looks like.
    const reduceAt = [...css.matchAll(/@media\s*\(prefers-reduced-motion:\s*reduce\)\s*\{/g)].map(m => m.index + m[0].length)
    expect(reduceAt.length).toBeGreaterThan(0)
    const blockBody = start => {
      let depth = 1, i = start
      for (; i < css.length && depth > 0; i++) depth += css[i] === '{' ? 1 : css[i] === '}' ? -1 : 0
      return css.slice(start, i - 1)
    }
    const reduceBodies = reduceAt.map(blockBody)
    // The element a selector styles: the first class of its last compound,
    // plus any pseudo-element (".bubble-overlay.closing" -> ".bubble-overlay").
    const subject = sel => {
      const last = sel.trim().split(/\s+/).pop()
      return (last.match(/^\.[\w-]+/)?.[0] || last) + (last.match(/::[\w-]+$/)?.[0] || '')
    }
    const covered = new Set()
    for (const body of reduceBodies) {
      for (const [, sels, decl] of body.matchAll(/([^{}]+)\{([^}]*)\}/g)) {
        if (/animation:\s*none\s*!important/.test(decl) && /transition:\s*none\s*!important/.test(decl)) {
          for (const sel of sels.split(',')) covered.add(subject(sel))
        }
      }
    }
    let outside = css
    for (const body of reduceBodies) outside = outside.replace(body, '')
    const rules = outside
      .replace(/\/\*[\s\S]*?\*\//g, '')
      .matchAll(/([^{}]+)\{([^}]*)\}/g)
    let checked = 0
    for (const [, sels, body] of rules) {
      if (!/\b(animation|transition)\s*:/.test(body) || sels.trim().startsWith('@')) continue
      for (const sel of sels.split(',')) {
        checked += 1
        expect(covered.has(subject(sel)), `${sel.trim()} animates but is not switched off`).toBe(true)
      }
    }
    expect(checked).toBeGreaterThan(5)
  })
})
