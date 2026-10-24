// @vitest-environment jsdom
import React, { useEffect } from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup, act, within, configure } from '@testing-library/react'
import '../../i18n.js'

// The first render in a file costs about 3 s while modules warm up (0.5-1 s after), so the 1 s waitFor
// and 5 s test defaults fail on a loaded machine without anything being wrong.
configure({ asyncUtilTimeout: 8000 })
vi.setConfig({ testTimeout: 20000 })

// The bubble as the one chat surface: creating a chat at an access level,
// the runtime scan, and what a screen reader is told. Ideas ported from the
// assistant panel's tests (live region, scan retry, runtime models, setup
// banner).
const { api, chatAccess, scan, listeners } = vi.hoisted(() => ({
  listeners: { chat: new Set() },
  chatAccess: { create: vi.fn(), setAccess: vi.fn(), record: vi.fn() },
  scan: vi.fn(),
  api: {
    getChatTurns: vi.fn(), getChatEvents: vi.fn(), startChatTurn: vi.fn(), stopChatTurn: vi.fn(), coderStatus: vi.fn(),
    coderWorkspaceList: vi.fn(), coderWorkspaceDefault: vi.fn(), getAgentRuntimeModels: vi.fn(), pickCoderFolder: vi.fn(),
    openPathWithOS: vi.fn(), getProfiles: vi.fn(), listChatConversations: vi.fn(), browserPermissions: vi.fn(), getActiveProfile: vi.fn(),
  },
}))
vi.mock('../../services/api.js', () => ({
  api, notify: vi.fn(),
  onChatEvent: cb => { listeners.chat.add(cb); return () => listeners.chat.delete(cb) },
  onOrgEvent: () => () => {}, onOrgRunStatus: () => () => {}, newOrgEventsStreamId: () => 'stream',
}))
vi.mock('../../lib/chatAccessApi.js', () => ({ chatAccessApi: chatAccess }))
vi.mock('../../lib/agentRuntimes.js', async importOriginal => ({ ...(await importOriginal()), cachedAgentScan: (...a) => scan(...a) }))
vi.mock('../ConfirmDialog.jsx', () => ({ confirm: vi.fn() }))

import CoderBubbles from './CoderBubbles.jsx'
import { useCoderBubbles } from './useCoderBubbles.js'

let store
function Harness({ seed }) {
  store = useCoderBubbles()
  useEffect(() => { if (seed !== undefined) store.openNewChat(seed) }, []) // eslint-disable-line react-hooks/exhaustive-deps
  return <CoderBubbles store={store} onNavigate={navigate} />
}
const navigate = vi.fn()
const emit = ev => act(() => { for (const cb of [...listeners.chat]) cb(ev) })
const overlay = () => screen.queryByTestId('bubble-overlay')
const box = () => within(overlay()).getByPlaceholderText('Type a message...')
const installed = [{ id: 'claude', installed: true, binary: '/bin/claude' }, { id: 'gemini', installed: true, binary: '/bin/gemini' }, { id: 'aider', installed: false }]
const coderOn = { enabled: true, ready: true, defaultAccess: 'coding', runtimes: [{ id: 'claude', ready: true, effort: true }, { id: 'gemini', ready: false, installed: true }] }

beforeEach(() => {
  vi.clearAllMocks()
  Element.prototype.scrollTo = function scrollTo() {}
  listeners.chat.clear()
  localStorage.clear()
  api.getChatTurns.mockResolvedValue({ items: [] })
  api.getChatEvents.mockResolvedValue({ items: [], hasMore: false })
  api.coderStatus.mockResolvedValue({ enabled: false })
  api.coderWorkspaceList.mockResolvedValue([])
  api.coderWorkspaceDefault.mockResolvedValue({ path: '/home/u/profile', created: false })
  api.getAgentRuntimeModels.mockResolvedValue([{ id: 'opus', label: 'Opus', effort_levels: ['low', 'high'] }])
  api.startChatTurn.mockResolvedValue({ ok: true })
  api.getProfiles.mockResolvedValue([])
  api.getActiveProfile.mockResolvedValue({ name: 'Work', root_dir: '/home/u/profile' })
  api.listChatConversations.mockResolvedValue({ items: [] })
  scan.mockResolvedValue({ agents: installed })
  chatAccess.record.mockResolvedValue({ access: 'scoped', title: '' })
  chatAccess.create.mockImplementation(async (_w, runtimeId, model, effort, access) => ({ id: 'c9', runtimeId, model, effort, access, cwd: access === 'scoped' ? '' : '/home/u/profile' }))
})
afterEach(cleanup)

describe('a new chat at an access level', () => {
  it('starts scoped when coder is off, on any installed runtime, with the tool settings', async () => {
    render(<Harness seed={undefined} />)
    fireEvent.click(await screen.findByLabelText('New chat'))
    const runtime = await screen.findByLabelText('Runtime')
    expect([...runtime.querySelectorAll('option')].map(o => o.value)).toEqual(['claude', 'gemini'])
    await waitFor(() => expect(screen.getByLabelText('Model')).toHaveValue('opus'))
    expect(api.getAgentRuntimeModels).toHaveBeenCalledWith('claude', '/bin/claude')
    expect(screen.getByLabelText('Access')).toHaveValue('scoped')
    // No folder picker for a scoped chat.
    expect(screen.queryByTestId('folder-picker')).toBeNull()

    fireEvent.change(screen.getByLabelText('Effort'), { target: { value: 'high' } })
    fireEvent.change(box(), { target: { value: 'hello' } })
    fireEvent.keyDown(box(), { key: 'Enter' })
    await waitFor(() => expect(chatAccess.create).toHaveBeenCalledWith('general', 'claude', 'opus', 'high', 'scoped', ''))
    expect(api.coderWorkspaceDefault).not.toHaveBeenCalled()
    // Tools and workflow runs default ON for a scoped turn, as in the assistant panel.
    await waitFor(() => expect(api.startChatTurn).toHaveBeenCalledWith('c9', expect.any(String), 'hello', true, true))
    await waitFor(() => expect(store.bubbles[0]).toMatchObject({ conversationId: 'c9', access: 'scoped' }))
    expect(JSON.parse(localStorage.getItem('monoagent:coderBubbles:v1')).bubbles[0].access).toBe('scoped')
    // The title is the backend's redacted one, read after the turn, never the raw prompt.
    expect(store.bubbles[0].title).toBe('')
    chatAccess.record.mockResolvedValue({ access: 'scoped', title: 'Greeting' })
    const turnId = api.startChatTurn.mock.calls[0][1]
    emit({ conversationId: 'c9', turnId, seq: 1, type: 'turn.started', payload: {} })
    emit({ conversationId: 'c9', turnId, seq: 2, type: 'turn.finished', payload: { status: 'completed' } })
    await waitFor(() => expect(store.bubbles[0].title).toBe('Greeting'))
  })

  it('sends a scoped turn without tools when Settings turned them off', async () => {
    localStorage.setItem('monoagent:assistantTools', '0')
    render(<Harness seed={undefined} />)
    fireEvent.click(await screen.findByLabelText('New chat'))
    await waitFor(() => expect(screen.getByLabelText('Model')).toHaveValue('opus'))
    fireEvent.change(box(), { target: { value: 'hi' } })
    fireEvent.keyDown(box(), { key: 'Enter' })
    await waitFor(() => expect(api.startChatTurn).toHaveBeenCalledWith('c9', expect.any(String), 'hi', false, false))
  })

  it('offers only scoped while coder is off, with the reason and a Settings link, and never enables coder', async () => {
    render(<Harness seed={undefined} />)
    fireEvent.click(await screen.findByLabelText('New chat'))
    const access = await screen.findByLabelText('Access')
    for (const level of ['coding', 'workspace', 'full']) expect(within(access).getByRole('option', { name: new RegExp(level, 'i') })).toBeDisabled()
    expect(within(access).getByRole('option', { name: 'Scoped' })).not.toBeDisabled()
    expect(screen.getByTestId('coder-disabled')).toHaveTextContent('Coder mode is off in Settings')
    fireEvent.click(within(screen.getByTestId('coder-disabled')).getByRole('button', { name: /settings/i }))
    expect(navigate).toHaveBeenCalledWith('settings')
  })

  it('takes the operator default level when coder is ready, on a coder-ready runtime, in the profile folder', async () => {
    api.coderStatus.mockResolvedValue(coderOn)
    render(<Harness seed={undefined} />)
    fireEvent.click(await screen.findByLabelText('New chat'))
    await waitFor(() => expect(screen.getByLabelText('Access')).toHaveValue('coding'))
    const runtime = await screen.findByLabelText('Runtime')
    expect([...runtime.querySelectorAll('option')].map(o => o.value)).toEqual(['claude'])
    expect(scan).not.toHaveBeenCalled() // the slow scan is only for scoped chats
    expect(screen.getByTestId('folder-picker')).toBeInTheDocument()
    await waitFor(() => expect(screen.getByLabelText('Model')).toHaveValue('opus'))
    fireEvent.change(box(), { target: { value: 'fix it' } })
    fireEvent.keyDown(box(), { key: 'Enter' })
    await waitFor(() => expect(chatAccess.create).toHaveBeenCalledWith('general', 'claude', 'opus', '', 'coding', '/home/u/profile'))
    expect(api.coderWorkspaceDefault).toHaveBeenCalledWith('claude')
    // A coding turn never gets monoagent tools.
    await waitFor(() => expect(api.startChatTurn).toHaveBeenCalledWith('c9', expect.any(String), 'fix it', false, false))
  })

  it('a plain click on New chat opens a full-access coder chat when the effective default is full, and the user can still pick another level', async () => {
    api.coderStatus.mockResolvedValue({ ...coderOn, defaultAccess: 'full' })
    render(<Harness seed={undefined} />)
    fireEvent.click(await screen.findByLabelText('New chat'))
    await waitFor(() => expect(screen.getByLabelText('Access')).toHaveValue('full'))
    expect(screen.getByTestId('folder-picker')).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('Access'), { target: { value: 'coding' } })
    await waitFor(() => expect(screen.getByLabelText('Access')).toHaveValue('coding'))
    fireEvent.change(screen.getByLabelText('Access'), { target: { value: 'full' } })
    await waitFor(() => expect(screen.getByLabelText('Access')).toHaveValue('full'))
    await waitFor(() => expect(screen.getByLabelText('Model')).toHaveValue('opus'))
    fireEvent.change(box(), { target: { value: 'go' } })
    fireEvent.keyDown(box(), { key: 'Enter' })
    await waitFor(() => expect(chatAccess.create).toHaveBeenCalledWith('general', 'claude', 'opus', '', 'full', '/home/u/profile'))
  })

  it('switching the level to scoped swaps the runtime list for the installed ones', async () => {
    api.coderStatus.mockResolvedValue(coderOn)
    render(<Harness seed={undefined} />)
    fireEvent.click(await screen.findByLabelText('New chat'))
    await waitFor(() => expect(screen.getByLabelText('Access')).toHaveValue('coding'))
    fireEvent.change(screen.getByLabelText('Access'), { target: { value: 'scoped' } })
    await waitFor(() => expect([...screen.getByLabelText('Runtime').querySelectorAll('option')].map(o => o.value)).toEqual(['claude', 'gemini']))
    expect(screen.queryByTestId('folder-picker')).toBeNull()
  })

  it('seeds the runtime from the entry point (the Agents page)', async () => {
    render(<Harness seed={{ setup: { runtime: 'gemini' } }} />)
    await waitFor(() => expect(screen.getByLabelText('Runtime')).toHaveValue('gemini'))
    // The models load is deferred a microtask after the runtime shows, so wait for it.
    await waitFor(() => expect(api.getAgentRuntimeModels).toHaveBeenCalledWith('gemini', '/bin/gemini'))
  })
})

describe('runtime scan and setup banner', () => {
  it('says monomind is missing and blocks sending, with the setup link', async () => {
    scan.mockResolvedValue({ error: 'monomind not found on PATH' })
    render(<Harness seed={undefined} />)
    fireEvent.click(await screen.findByLabelText('New chat'))
    const banner = await screen.findByTestId('setup-blocked')
    expect(banner).toHaveTextContent('monomind not found')
    expect(banner).toHaveTextContent('npm install -g @monoes/monomindcli')
    expect(box()).toBeDisabled()
  })

  it('shows another error as is, and a retry scans again and recovers', async () => {
    scan.mockResolvedValueOnce({ error: 'handshake failed (exit 127)' })
    render(<Harness seed={undefined} />)
    fireEvent.click(await screen.findByLabelText('New chat'))
    expect(await screen.findByTestId('setup-blocked')).toHaveTextContent('handshake failed (exit 127)')
    fireEvent.click(screen.getByRole('button', { name: 'Scan again' }))
    await waitFor(() => expect(screen.getByLabelText('Runtime')).toHaveValue('claude'))
    expect(scan).toHaveBeenCalledTimes(2)
    expect(screen.queryByTestId('setup-blocked')).toBeNull()
  })

  it('names the case of no installed agent', async () => {
    scan.mockResolvedValue({ agents: [{ id: 'claude', installed: false }] })
    render(<Harness seed={undefined} />)
    fireEvent.click(await screen.findByLabelText('New chat'))
    const blocked = await screen.findByTestId('setup-blocked')
    expect(blocked).toHaveTextContent('No AI agent is installed.')
    fireEvent.click(within(blocked).getByRole('button', { name: /set up ai agents/i }))
    expect(navigate).toHaveBeenCalledWith('ai')
  })

  it('a runtime with no models blocks sending', async () => {
    api.getAgentRuntimeModels.mockResolvedValue([])
    render(<Harness seed={undefined} />)
    fireEvent.click(await screen.findByLabelText('New chat'))
    expect(await screen.findByTestId('setup-blocked')).toHaveTextContent('reports no models')
    expect(box()).toBeDisabled()
  })

  it('announces the composer reason change through the live region', async () => {
    render(<Harness seed={undefined} />)
    fireEvent.click(await screen.findByLabelText('New chat'))
    const live = await screen.findByRole('status', { name: 'Chat turn announcements' })
    await waitFor(() => expect(live).toHaveTextContent('You can send a message now.'))
  })
})

describe('titles', () => {
  it('never puts a raw first prompt in the title or in storage', async () => {
    render(<Harness seed={undefined} />)
    fireEvent.click(await screen.findByLabelText('New chat'))
    await waitFor(() => expect(screen.getByLabelText('Model')).toHaveValue('opus'))
    fireEvent.change(box(), { target: { value: 'password: hunter2 please deploy' } })
    fireEvent.keyDown(box(), { key: 'Enter' })
    await waitFor(() => expect(api.startChatTurn).toHaveBeenCalled())
    await waitFor(() => expect(store.bubbles[0].conversationId).toBe('c9'))
    expect(store.bubbles[0].title).toBe('')
    expect(localStorage.getItem('monoagent:coderBubbles:v1')).not.toContain('hunter2')
    expect(document.querySelector('[data-bubble]').getAttribute('aria-label')).not.toContain('hunter2')
    // The backend's redacted title is what shows after the turn.
    chatAccess.record.mockResolvedValue({ access: 'scoped', title: 'Deploy request' })
    const turnId = api.startChatTurn.mock.calls[0][1]
    emit({ conversationId: 'c9', turnId, seq: 1, type: 'turn.started', payload: {} })
    emit({ conversationId: 'c9', turnId, seq: 2, type: 'turn.finished', payload: { status: 'completed' } })
    await waitFor(() => expect(store.bubbles[0].title).toBe('Deploy request'))
    expect(localStorage.getItem('monoagent:coderBubbles:v1')).not.toContain('hunter2')
  })
})

describe('turn announcements', () => {
  it('announces a finished turn once, with a failed-tool count, through a persistent region', async () => {
    api.getChatTurns.mockResolvedValue({ items: [] })
    render(<Harness seed={undefined} />)
    fireEvent.click(await screen.findByLabelText('New chat'))
    await waitFor(() => expect(screen.getByLabelText('Model')).toHaveValue('opus'))
    fireEvent.change(box(), { target: { value: 'go' } })
    fireEvent.keyDown(box(), { key: 'Enter' })
    await waitFor(() => expect(api.startChatTurn).toHaveBeenCalled())
    const turnId = api.startChatTurn.mock.calls[0][1]
    const live = screen.getByRole('status', { name: 'Chat turn announcements' })
    emit({ conversationId: 'c9', turnId, seq: 1, type: 'turn.started', payload: {} })
    emit({ conversationId: 'c9', turnId, seq: 2, type: 'tool.started', payload: { callId: 'k1', name: 'Bash', arguments: '{}' } })
    emit({ conversationId: 'c9', turnId, seq: 3, type: 'tool.completed', payload: { callId: 'k1', ok: false, result: 'x' } })
    emit({ conversationId: 'c9', turnId, seq: 4, type: 'turn.finished', payload: { status: 'completed' } })
    await waitFor(() => expect(live).toHaveTextContent(/^Response completed\./))
    expect(live).toHaveTextContent('1 tool call failed.')
  })
})
