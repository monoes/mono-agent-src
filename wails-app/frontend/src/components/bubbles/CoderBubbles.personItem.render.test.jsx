// @vitest-environment jsdom
// The person items reach the agent's pane and the dock tray.
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

import CoderBubbles from './CoderBubbles.jsx'
import { PersonItemsProvider } from './PersonItemsContext.jsx'
import { useCoderBubbles } from './useCoderBubbles.js'

let store
let personValue
function Harness() {
  store = useCoderBubbles()
  useEffect(() => { store.openConversation({ id: 'root', cwd: '/home/u/work/mission', model: 'opus', access: 'full' }, { expand: false }) }, []) // eslint-disable-line react-hooks/exhaustive-deps
  return <PersonItemsProvider value={personValue}><CoderBubbles store={store} onNavigate={() => {}} /></PersonItemsProvider>
}

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
  mockConfirm.mockResolvedValue(true)
})
afterEach(cleanup)

const plan = (sessionId, conversationId, extra = {}) => ({ id: sessionId, kind: 'plan_approval', sessionId, conversationId, leadConversationId: 'root', title: `Plan of ${sessionId}`, reasons: ['30 tasks, over 25'], taskCount: 30, budgetUsd: 5, ...extra })
const value = items => ({ items, loaded: true, refresh: vi.fn(), answer: vi.fn().mockResolvedValue({}), openAgent: vi.fn() })
const openRoot = async () => {
  render(<Harness />)
  fireEvent.click(await waitFor(() => { const b = document.querySelector('[data-bubble="root"]'); if (!b) throw new Error('no bubble'); return b }))
  return screen.findByTestId('bubble-overlay')
}

describe('person items in the bubbles', () => {
  it("shows the answer card of the chat's own session in its pane and answers from it", async () => {
    personValue = value([plan(1, 'root')])
    const pane = await openRoot()
    expect(await within(pane).findByRole('group', { name: /Plan of 1/ })).toBeInTheDocument()
    fireEvent.click(within(pane).getByRole('button', { name: /Approve/ }))
    expect(personValue.answer).toHaveBeenCalledWith('approve', expect.objectContaining({ sessionId: 1 }), undefined)
  })

  it('shows no card in a pane whose chat has no item', async () => {
    personValue = value([plan(3, 'conv-3')])
    const pane = await openRoot()
    expect(within(pane).queryByTestId('person-item-card')).toBeNull()
  })

  it('puts a tray in the dock that opens the cards', async () => {
    personValue = value([plan(3, 'conv-3')])
    render(<Harness />)
    fireEvent.click(await screen.findByRole('button', { name: '1 item needs you' }))
    expect(screen.getByTestId('person-item-card')).toBeInTheDocument()
  })

  it('shows no tray with nothing waiting', async () => {
    personValue = value([])
    render(<Harness />)
    await waitFor(() => expect(document.querySelector('[data-bubble="root"]')).not.toBeNull())
    expect(screen.queryByTestId('person-tray')).toBeNull()
  })
})
