// @vitest-environment jsdom
// A reopened conversation whose latest turn is still running in THIS window
// goes back to the live stream, not a snapshot (ported from PR #963, which
// fixed it in the old assistant panel). A turn another window owns, and a
// finished one, stay snapshots.
import React, { useEffect } from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, waitFor, cleanup, within } from '@testing-library/react'
import '../../i18n.js'

const { api, chatAccess } = vi.hoisted(() => ({
  chatAccess: { create: vi.fn(), setAccess: vi.fn(), record: vi.fn() },
  api: {
    getChatMissionToggle: vi.fn(() => Promise.resolve({})), // the Mission switch in the pane header
    getChatTurns: vi.fn(), getChatEvents: vi.fn(), startChatTurn: vi.fn(), stopChatTurn: vi.fn(), coderStatus: vi.fn(),
    coderWorkspaceList: vi.fn(), coderWorkspaceDefault: vi.fn(), createCoderConversation: vi.fn(), getAgentRuntimeModels: vi.fn(),
    pickCoderFolder: vi.fn(), openPathWithOS: vi.fn(), getProfiles: vi.fn(),
  },
}))
vi.mock('../../services/api.js', () => ({
  api, notify: vi.fn(), onChatEvent: () => () => {}, onOrgEvent: () => () => {}, onOrgRunStatus: () => () => {}, newOrgEventsStreamId: () => 'stream',
}))
vi.mock('../../lib/chatAccessApi.js', () => ({ chatAccessApi: chatAccess }))
vi.mock('../ConfirmDialog.jsx', () => ({ confirm: vi.fn() }))

import CoderBubbles from './CoderBubbles.jsx'
import { useCoderBubbles } from './useCoderBubbles.js'

function Harness() {
  const store = useCoderBubbles()
  useEffect(() => { store.openConversation({ id: 'coder-9', cwd: '/home/u/monoagent-coder/x', model: 'opus' }) }, []) // eslint-disable-line react-hooks/exhaustive-deps
  return <CoderBubbles store={store} onNavigate={() => {}} />
}

const events = turnId => ({ items: [
  { conversationId: 'coder-9', turnId, seq: 1, at: '2026-10-23T10:00:00.000Z', type: 'turn.started', payload: { backend: 'agent', text: 'still running' } },
  { conversationId: 'coder-9', turnId, seq: 2, at: '2026-10-23T10:00:01.000Z', type: 'assistant.delta', payload: { text: 'partial answer' } },
], hasMore: false })

beforeEach(() => {
  vi.clearAllMocks()
  Element.prototype.scrollTo = function scrollTo() {}
  localStorage.clear()
  api.getChatEvents.mockResolvedValue({ items: [], hasMore: false })
  api.coderStatus.mockResolvedValue({ enabled: true, ready: true, defaultAccess: 'full', workspaceRoot: '/home/u/monoagent-coder' })
  api.coderWorkspaceList.mockResolvedValue([])
  api.getAgentRuntimeModels.mockResolvedValue([{ id: 'opus', label: 'Opus 5.5', effort_levels: [] }])
  api.getProfiles.mockResolvedValue([])
  chatAccess.record.mockResolvedValue({ access: 'full', mode: 'coder' })
})
afterEach(cleanup)

const stop = () => screen.queryByRole('button', { name: 'Stop generating' })
const overlay = () => screen.findByTestId('bubble-overlay')

describe('a reopened conversation with a turn still running', () => {
  it('goes back to the live stream when this window owns it: events read, Stop shown, no finished snapshot', async () => {
    api.getChatTurns.mockResolvedValue({ items: [
      { id: 't2', prompt: 'still running', status: 'active', ownedByThisInstance: true },
      { id: 't1', prompt: 'earlier prompt', status: 'completed' },
    ] })
    api.getChatEvents.mockImplementation((_c, turn) => Promise.resolve(turn === 't2' ? events('t2') : { items: [], hasMore: false }))
    render(<Harness />)
    const o = await overlay()
    await within(o).findByText('still running')
    await waitFor(() => expect(api.getChatEvents).toHaveBeenCalledWith('coder-9', 't2', 0, 200))
    expect(await screen.findByRole('button', { name: 'Stop generating' })).toBeInTheDocument()
    expect(screen.queryByText('Running in another window')).toBeNull()
    // It is the live turn (the supervisor's own stream), not a loaded snapshot of it.
    expect(api.getChatEvents.mock.calls.filter(c => c[1] === 't2').length).toBeGreaterThan(0)
  })

  it('stays a snapshot, labelled "Running in another window", when another window owns it', async () => {
    api.getChatTurns.mockResolvedValue({ items: [{ id: 't2', prompt: 'still running', status: 'active', ownedByThisInstance: false }] })
    api.getChatEvents.mockResolvedValue(events('t2'))
    render(<Harness />)
    const o = await overlay()
    await within(o).findByText('still running')
    expect(await within(o).findByText('Running in another window')).toBeInTheDocument()
    expect(stop()).toBeNull()
  })

  it('shows a completed turn as a snapshot with no Stop', async () => {
    api.getChatTurns.mockResolvedValue({ items: [{ id: 't1', prompt: 'earlier prompt', status: 'completed' }] })
    api.getChatEvents.mockResolvedValue({ items: [
      { conversationId: 'coder-9', turnId: 't1', seq: 1, at: '2026-10-23T10:00:00.000Z', type: 'turn.started', payload: { backend: 'agent', text: 'earlier prompt' } },
      { conversationId: 'coder-9', turnId: 't1', seq: 2, at: '2026-10-23T10:00:01.000Z', type: 'turn.finished', payload: { status: 'completed' } },
    ], hasMore: false })
    render(<Harness />)
    const o = await overlay()
    await within(o).findByText('earlier prompt')
    await waitFor(() => expect(api.getChatEvents).toHaveBeenCalledWith('coder-9', 't1', 0, 200))
    expect(stop()).toBeNull()
    expect(screen.queryByText('Running in another window')).toBeNull()
  })
})
