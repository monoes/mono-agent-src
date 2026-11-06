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
    getChatMissionToggle: vi.fn(() => Promise.resolve({})), // the Mission switch in a full pane's header
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
function Harness() {
  store = useCoderBubbles()
  useEffect(() => { store.openConversation({ id: 'front', cwd: '/home/u/work/open', model: 'opus', access: 'full' }) }, []) // eslint-disable-line react-hooks/exhaustive-deps
  return <CoderBubbles store={store} onNavigate={navigate} />
}

const B = (extra = {}) => ({ id: 9, parent_session_id: 0, conversation_id: 'boss-conv', title: 'Boss', state: 'running', counts: {}, boss: true, client_id: 'boss-for-front', ...extra })
const modeOf = mode => ({ chatMode: { mode, locked: true, reason: '', options: [] } })

beforeEach(() => {
  vi.clearAllMocks()
  Element.prototype.scrollTo = function scrollTo() {}
  listeners.chat.clear()
  localStorage.clear()
  api.getChatTurns.mockResolvedValue({ items: [] })
  api.getChatEvents.mockResolvedValue({ items: [], hasMore: false })
  api.coderStatus.mockResolvedValue({ enabled: true, ready: true, defaultAccess: 'full', workspaceRoot: '/w' })
  api.coderWorkspaceList.mockResolvedValue([])
  api.getAgentRuntimeModels.mockResolvedValue([])
  api.getProfiles.mockResolvedValue([])
  chatAccess.record.mockResolvedValue({ access: 'full', mode: 'coder' })
  sessions.show.mockResolvedValue({}); sessions.board.mockResolvedValue({}); sessions.pulse.mockResolvedValue({})
})
afterEach(cleanup)

// The overlay hands its chat view the steering switch: the composer steers only while the chat's mode is open AND a
// recorded boss stands behind it.
describe('the overlay\'s steering wiring', () => {
  const mount = async (mode, rows) => {
    api.getChatMissionToggle.mockResolvedValue(modeOf(mode))
    sessions.list.mockResolvedValue({ sessions: rows })
    render(<Harness />)
    await screen.findByTestId('bubble-overlay')
    await waitFor(() => expect(sessions.list).toHaveBeenCalled())
    await waitFor(() => expect(api.getChatMissionToggle).toHaveBeenCalled())
    // let the mode and the sessions settle into the overlay
    await act(async () => { await new Promise(r => setTimeout(r, 30)) })
  }
  it('steers in an open chat that has its boss', async () => {
    await mount('open', [B()])
    expect(await screen.findByTestId('steering-bar')).toBeInTheDocument()
  })
  it('does not steer in an open chat with no boss yet', async () => {
    await mount('open', [])
    expect(screen.queryByTestId('steering-bar')).toBeNull()
  })
  it('does not steer when the boss is only a session carrying the key', async () => {
    await mount('open', [B({ boss: false })])
    expect(screen.queryByTestId('steering-bar')).toBeNull()
  })
  it('does not steer in a chat of another mode, boss or not', async () => {
    await mount('mission', [B()])
    expect(screen.queryByTestId('steering-bar')).toBeNull()
    cleanup()
    await mount('off', [B()])
    expect(screen.queryByTestId('steering-bar')).toBeNull()
  })
})
