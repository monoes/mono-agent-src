// @vitest-environment jsdom
// The person items reach the orbit, the agent's pane (lite and root) and the dock.
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
    getChatMissionToggle: vi.fn(() => Promise.resolve({})), // the Mission switch in the pane header
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
import { PersonItemsProvider } from './PersonItemsContext.jsx'
import { useCoderBubbles } from './useCoderBubbles.js'

let store
let personValue
const navigate = vi.fn()
function Harness({ openRoot = true }) {
  store = useCoderBubbles()
  useEffect(() => { if (openRoot) store.openConversation({ id: 'root', cwd: '/home/u/work/mission', model: 'opus', access: 'full' }, { expand: false }) }, []) // eslint-disable-line react-hooks/exhaustive-deps
  return <PersonItemsProvider value={personValue}><CoderBubbles store={store} onNavigate={navigate} /></PersonItemsProvider>
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


const plan = (sessionId, conversationId, extra = {}) => ({ id: sessionId, kind: 'plan_approval', sessionId, conversationId, leadConversationId: 'root', title: `Plan of ${sessionId}`, reasons: ['30 tasks, over 25'], taskCount: 30, budgetUsd: 5, ...extra })
const value = items => ({ items, loaded: true, refresh: vi.fn(), answer: vi.fn().mockResolvedValue({}), openAgent: vi.fn() })

describe('person items in the bubbles', () => {
  it('lights the circle of the agent whose plan waits, and no other', async () => {
    personValue = value([plan(3, 'conv-3')])
    render(<Harness />)
    const b = await screen.findByRole('button', { name: 'Direction B, Waiting for you, 1 item needs you' })
    expect(b).toHaveAttribute('data-needs-you', 'true')
    expect(screen.getByRole('button', { name: 'Direction A, Running, 1 agent of its own' })).not.toHaveAttribute('data-needs-you')
  })

  it('shows the answer card in that agent\'s simple pane, and not in another\'s', async () => {
    personValue = value([plan(3, 'conv-3')])
    render(<Harness />)
    fireEvent.click(await screen.findByRole('button', { name: /^Direction B/ }))
    const pane = await screen.findByTestId('bubble-overlay')
    expect(await within(pane).findByRole('group', { name: /Plan of 3/ })).toBeInTheDocument()
    fireEvent.click(within(pane).getByRole('button', { name: /Approve/ }))
    expect(personValue.answer).toHaveBeenCalledWith('approve', expect.objectContaining({ sessionId: 3 }), undefined)
  })

  it('shows no card in a pane whose agent has no item', async () => {
    personValue = value([plan(3, 'conv-3')])
    render(<Harness />)
    fireEvent.click(await screen.findByRole('button', { name: /^Direction A/ }))
    const pane = await screen.findByTestId('bubble-overlay')
    expect(within(pane).queryByTestId('person-item-card')).toBeNull()
  })

  it('shows the card in a full (root) pane too', async () => {
    personValue = value([plan(1, 'root')])
    render(<Harness />)
    await screen.findByRole('button', { name: /^Direction A/ })
    fireEvent.click(document.querySelector('[data-bubble="root"]'))
    const pane = await screen.findByTestId('bubble-overlay')
    expect(await within(pane).findByTestId('person-item-card')).toBeInTheDocument()
  })

  it('puts a tray in the dock that opens the cards', async () => {
    personValue = value([plan(3, 'conv-3')])
    render(<Harness />)
    fireEvent.click(await screen.findByRole('button', { name: '1 plan needs you' }))
    expect(screen.getByTestId('person-item-card')).toBeInTheDocument()
  })

  it('shows no tray and no mark with nothing waiting', async () => {
    personValue = value([])
    render(<Harness />)
    await screen.findByRole('button', { name: /^Direction A/ })
    expect(screen.queryByTestId('person-tray')).toBeNull()
    expect(document.querySelector('[data-needs-you]')).toBeNull()
  })
})
