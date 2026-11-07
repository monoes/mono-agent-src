// @vitest-environment jsdom
import React, { useEffect } from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, renderHook, screen, fireEvent, waitFor, cleanup, act, within } from '@testing-library/react'
import '../../i18n.js'

const { api, chatAccess, listeners, mockConfirm, sessions, seen } = vi.hoisted(() => ({
  seen: { props: null },
  sessions: { list: vi.fn(), show: vi.fn(), board: vi.fn(), pulse: vi.fn(), pause: vi.fn(), resume: vi.fn(), stop: vi.fn() },
  listeners: { chat: new Set() },
  chatAccess: { create: vi.fn(), setAccess: vi.fn(), record: vi.fn() },
  mockConfirm: vi.fn(),
  api: {
    getChatMissionToggle: vi.fn(() => Promise.resolve({})), // the Mission switch in a full pane's header
    getChatTurns: vi.fn(),
    getChatEvents: vi.fn(),
    startChatTurn: vi.fn(),
    steerBoss: vi.fn(),
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

vi.mock('./CoderChatView.jsx', () => ({ CoderChatView: props => { seen.props = props; return null } }))
import CoderBubbles from './CoderBubbles.jsx'
import { useCoderBubbles } from './useCoderBubbles.js'

let store
const navigate = vi.fn()
function Harness({ id = 'front' }) {
  store = useCoderBubbles()
  useEffect(() => { store.openConversation({ id, cwd: '/home/u/work/open', model: 'opus', access: 'full' }) }, []) // eslint-disable-line react-hooks/exhaustive-deps
  return <CoderBubbles store={store} onNavigate={navigate} />
}

const B = (extra = {}) => ({ id: 9, parent_session_id: 0, conversation_id: 'boss-conv', title: 'Boss', state: 'running', counts: {}, boss: true, client_id: 'boss-for-front-1', ...extra })
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

// The overlay gives its chat view the chat that steering goes to (steerChat) only while the chat's mode is open AND an open
// boss stands behind it; in a boss's own conversation it gives the chat behind that boss, flagged direct; in every other
// case it gives none, so the chat view sends ordinary turns.
describe('the overlay passes steerChat to the chat view', () => {
  const mount = async (id, mode, rows) => {
    seen.props = null
    api.getChatMissionToggle.mockResolvedValue(modeOf(mode))
    sessions.list.mockResolvedValue({ sessions: rows })
    render(<Harness id={id} />)
    await screen.findByTestId('bubble-overlay')
    await waitFor(() => expect(sessions.list).toHaveBeenCalled())
    await waitFor(() => expect(api.getChatMissionToggle).toHaveBeenCalled())
    await act(async () => { await new Promise(r => setTimeout(r, 30)) })
    return seen.props
  }
  it('names its own conversation while the mode is open and the boss is open', async () => {
    const p = await mount('front', 'open', [B()])
    expect(p.steerChat).toBe('front')
    expect(p.directBoss).toBe(false)
  })
  it('names none without a boss, with a finished boss, with an unstamped one, or in another mode', async () => {
    for (const [mode, rows] of [['open', []], ['open', [B({ state: 'done' })]], ['open', [B({ boss: false })]], ['mission', [B()]], ['off', [B()]]]) {
      cleanup()
      const p = await mount('front', mode, rows)
      expect(p.steerChat).toBe('')
    }
  })
  it('names the chat behind a boss in the boss\'s own conversation, flagged direct, whatever the mode', async () => {
    const p = await mount('boss-conv', 'off', [B({ state: 'done' })])
    expect(p.steerChat).toBe('front')
    expect(p.directBoss).toBe(true)
  })
})
