// @vitest-environment jsdom
import React, { useEffect } from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup, act, within, configure } from '@testing-library/react'
import '../../i18n.js'

configure({ asyncUtilTimeout: 8000 })
vi.setConfig({ testTimeout: 20000 })

// Clear and Compact on a coder bubble: the bubble goes back to a draft, the
// next message makes a new conversation, and Compact opens it with the summary.
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
import { COMPACT_PROMPT } from '../../lib/chatCompact.js'

let store
function Harness() {
  store = useCoderBubbles()
  useEffect(() => { store.openNewChat() }, []) // eslint-disable-line react-hooks/exhaustive-deps
  return <CoderBubbles store={store} onNavigate={vi.fn()} />
}
const emit = ev => act(() => { for (const cb of [...listeners.chat]) cb(ev) })
const box = () => within(screen.getByTestId('bubble-overlay')).getByPlaceholderText('Type a message...')
const sendText = text => { fireEvent.change(box(), { target: { value: text } }); fireEvent.keyDown(box(), { key: 'Enter' }) }
const finish = (conversationId, turnId, text) => {
  emit({ conversationId, turnId, seq: 1, type: 'turn.started', payload: {} })
  if (text) emit({ conversationId, turnId, seq: 2, type: 'assistant.delta', payload: { partId: 'p1', text } })
  emit({ conversationId, turnId, seq: 3, type: 'turn.finished', payload: { status: 'completed' } })
}

beforeEach(() => {
  vi.clearAllMocks()
  Element.prototype.scrollTo = function scrollTo() {}
  listeners.chat.clear()
  localStorage.clear()
  api.getChatTurns.mockResolvedValue({ items: [] })
  api.getChatEvents.mockResolvedValue({ items: [], hasMore: false })
  api.coderStatus.mockResolvedValue({ enabled: false })
  api.coderWorkspaceList.mockResolvedValue([])
  api.getAgentRuntimeModels.mockResolvedValue([{ id: 'opus', label: 'Opus', effort_levels: [] }])
  api.startChatTurn.mockResolvedValue({ ok: true })
  api.getProfiles.mockResolvedValue([])
  api.getActiveProfile.mockResolvedValue({ name: 'Work', root_dir: '/home/u/profile' })
  api.listChatConversations.mockResolvedValue({ items: [] })
  scan.mockResolvedValue({ agents: [{ id: 'claude', installed: true, binary: '/bin/claude' }] })
  chatAccess.record.mockResolvedValue({ access: 'scoped', title: '' })
  let n = 0
  chatAccess.create.mockImplementation(async (_w, runtimeId, model, effort, access) => ({ id: `c${++n}`, runtimeId, model, effort, access, cwd: '' }))
})
afterEach(cleanup)

async function firstTurn() {
  render(<Harness />)
  await waitFor(() => expect(screen.getByLabelText('Model')).toHaveValue('opus'))
  sendText('hello')
  await waitFor(() => expect(api.startChatTurn).toHaveBeenCalledTimes(1))
  await waitFor(() => expect(store.bubbles[0].conversationId).toBe('c1'))
  finish('c1', api.startChatTurn.mock.calls[0][1], 'Hi there')
  await waitFor(() => expect(screen.getByRole('button', { name: 'Clear' })).not.toBeDisabled())
}

describe('Clear and Compact on a coder bubble', () => {
  it('disables both buttons on a blank draft', async () => {
    render(<Harness />)
    await waitFor(() => expect(screen.getByLabelText('Model')).toHaveValue('opus'))
    expect(screen.getByRole('button', { name: 'Clear' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Compact' })).toBeDisabled()
  })

  it('Clear unbinds the conversation and the next message makes a new one', async () => {
    await firstTurn()
    fireEvent.click(screen.getByRole('button', { name: 'Clear' }))
    await waitFor(() => expect(store.bubbles[0].conversationId).toBe(''))
    await waitFor(() => expect(screen.getByLabelText('Model')).toHaveValue('opus'))
    sendText('again')
    await waitFor(() => expect(chatAccess.create).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(api.startChatTurn).toHaveBeenLastCalledWith('c2', expect.any(String), 'again', true, true))
  })

  it('Compact asks for a summary, then opens a new conversation with it', async () => {
    await firstTurn()
    fireEvent.click(screen.getByRole('button', { name: 'Compact' }))
    await waitFor(() => expect(api.startChatTurn).toHaveBeenCalledTimes(2))
    expect(api.startChatTurn.mock.calls[1][2]).toBe(COMPACT_PROMPT)
    finish('c1', api.startChatTurn.mock.calls[1][1], 'The summary text')
    await waitFor(() => expect(chatAccess.create).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(api.startChatTurn).toHaveBeenCalledTimes(3))
    const [convId, , prompt] = api.startChatTurn.mock.calls[2]
    expect(convId).toBe('c2')
    expect(prompt).toContain('The summary text')
    expect(store.bubbles[0].conversationId).toBe('c2')
  })
})
