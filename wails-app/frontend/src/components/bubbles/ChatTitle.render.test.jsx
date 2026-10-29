// @vitest-environment jsdom
import React, { useEffect } from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup, within } from '@testing-library/react'
import '../../i18n.js'

// The name at the top of a chat: double-click renames it, Enter or leaving the box saves, Esc puts
// the old name back (and does not collapse the chat), an empty name takes the typed one back.
const { api, chatAccess, budget } = vi.hoisted(() => ({
  chatAccess: { create: vi.fn(), setAccess: vi.fn(), record: vi.fn(), rename: vi.fn() },
  budget: { forTask: vi.fn(), list: vi.fn(), lower: vi.fn() },
  api: {
    getChatTurns: vi.fn(), getChatEvents: vi.fn(), startChatTurn: vi.fn(), stopChatTurn: vi.fn(), coderStatus: vi.fn(),
    coderWorkspaceList: vi.fn(), getAgentRuntimeModels: vi.fn(), getProfiles: vi.fn(), listChatConversations: vi.fn(),
    browserPermissions: vi.fn(), setChatOrgMode: vi.fn(), getActiveProfile: vi.fn(),
    getCoderSessionSettings: vi.fn(), setCoderSessionSettings: vi.fn(),
  },
}))
vi.mock('../../services/api.js', () => ({
  api, notify: vi.fn(), onChatEvent: () => () => {}, onOrgEvent: () => () => {}, onOrgRunStatus: () => () => {}, newOrgEventsStreamId: () => 's',
}))
vi.mock('../../lib/chatAccessApi.js', () => ({ chatAccessApi: chatAccess }))
vi.mock('../settings/aitaskBudgetApi.js', () => ({ aitaskBudgetApi: budget, money: n => `$${Number(n || 0).toFixed(2)}` }))

import CoderBubbles from './CoderBubbles.jsx'
import { useCoderBubbles } from './useCoderBubbles.js'

let store
function Harness({ conv, draft }) {
  store = useCoderBubbles()
  useEffect(() => { if (draft) store.openDraft({}); else store.openConversation(conv) }, []) // eslint-disable-line react-hooks/exhaustive-deps
  return <CoderBubbles store={store} onNavigate={vi.fn()} />
}
const overlay = () => screen.queryByTestId('bubble-overlay')
const open = async (conv) => {
  render(conv.draft ? <Harness draft /> : <Harness conv={conv} />)
  await waitFor(() => expect(overlay()).toBeInTheDocument())
}
const folderChat = { id: 'k1', access: 'coding', runtimeId: 'claude', model: 'opus', cwd: '/home/u/proj', verified: true }
const scopedChat = { id: 's1', access: 'scoped', runtimeId: 'claude', model: 'opus', title: 'Summarise my week' }
const titleEl = () => within(overlay()).getByTestId('chat-title')
const editor = () => within(overlay()).getByLabelText('Chat name')

beforeEach(() => {
  vi.clearAllMocks()
  Element.prototype.scrollTo = function scrollTo() {}
  localStorage.clear()
  api.getChatTurns.mockResolvedValue({ items: [] })
  api.getChatEvents.mockResolvedValue({ items: [], hasMore: false })
  api.coderStatus.mockResolvedValue({ enabled: true, ready: true })
  api.coderWorkspaceList.mockResolvedValue([])
  api.getProfiles.mockResolvedValue([])
  api.getActiveProfile.mockResolvedValue(null)
  api.getCoderSessionSettings.mockResolvedValue({ conversationId: 'k1', settings: {}, allowed: {} })
  api.listChatConversations.mockResolvedValue({ items: [] })
  api.browserPermissions.mockResolvedValue({ kinds: [], catalog: [] })
  budget.list.mockResolvedValue({ caps: [], spentUsd: 0 })
  chatAccess.record.mockImplementation(async id => (id === 's1' ? { access: 'scoped', title: 'Summarise my week' } : { access: 'coding', cwd: '/home/u/proj' }))
  chatAccess.rename.mockImplementation(async (_id, title) => title.trim())
})
afterEach(cleanup)

describe('renaming a chat from its title', () => {
  it('double-click opens a box with the name; Enter saves it and the new name shows', async () => {
    await open(scopedChat)
    expect(screen.queryByLabelText('Chat name')).toBeNull()
    fireEvent.doubleClick(titleEl())
    expect(editor()).toHaveValue('Summarise my week')
    fireEvent.change(editor(), { target: { value: 'weekly review' } })
    fireEvent.keyDown(editor(), { key: 'Enter' })
    await waitFor(() => expect(chatAccess.rename).toHaveBeenCalledWith('s1', 'weekly review'))
    await waitFor(() => expect(titleEl()).toHaveTextContent('weekly review'))
    expect(chatAccess.rename).toHaveBeenCalledTimes(1) // Enter, then the box going away, saved once
  })

  it('Esc puts the old name back, saves nothing and leaves the chat open', async () => {
    await open(scopedChat)
    fireEvent.doubleClick(titleEl())
    fireEvent.change(editor(), { target: { value: 'something else' } })
    fireEvent.keyDown(editor(), { key: 'Escape' })
    await waitFor(() => expect(screen.queryByLabelText('Chat name')).toBeNull())
    expect(titleEl()).toHaveTextContent('Summarise my week')
    expect(chatAccess.rename).not.toHaveBeenCalled()
    expect(overlay()).toBeInTheDocument()
  })

  it('leaving the box saves, and an unchanged name saves nothing', async () => {
    await open(scopedChat)
    fireEvent.doubleClick(titleEl())
    fireEvent.blur(editor())
    await waitFor(() => expect(screen.queryByLabelText('Chat name')).toBeNull())
    expect(chatAccess.rename).not.toHaveBeenCalled()
    fireEvent.doubleClick(titleEl())
    fireEvent.change(editor(), { target: { value: '  spaced out  ' } })
    fireEvent.blur(editor())
    await waitFor(() => expect(chatAccess.rename).toHaveBeenCalledWith('s1', 'spaced out'))
  })

  it('opens from the keyboard with F2 or Enter on the name', async () => {
    await open(scopedChat)
    fireEvent.keyDown(titleEl(), { key: 'F2' })
    expect(editor()).toBeInTheDocument()
    fireEvent.keyDown(editor(), { key: 'Escape' })
    fireEvent.keyDown(titleEl(), { key: 'Enter' })
    expect(editor()).toBeInTheDocument()
  })

  it('a name that is refused stays in the box with the reason', async () => {
    chatAccess.rename.mockRejectedValue(new Error('conversation not found'))
    await open(scopedChat)
    fireEvent.doubleClick(titleEl())
    fireEvent.change(editor(), { target: { value: 'nope' } })
    fireEvent.keyDown(editor(), { key: 'Enter' })
    expect(await screen.findByRole('alert')).toHaveTextContent('Not renamed: conversation not found')
    expect(editor()).toBeInTheDocument()
  })

  it('a chat in a folder takes the typed name, and an empty one gives the folder back', async () => {
    await open(folderChat)
    expect(titleEl()).toHaveTextContent('proj')
    fireEvent.doubleClick(titleEl())
    fireEvent.change(editor(), { target: { value: 'meta challenge' } })
    fireEvent.keyDown(editor(), { key: 'Enter' })
    await waitFor(() => expect(titleEl()).toHaveTextContent('meta challenge'))
    chatAccess.record.mockClear()
    fireEvent.doubleClick(titleEl())
    fireEvent.change(editor(), { target: { value: '' } })
    fireEvent.keyDown(editor(), { key: 'Enter' })
    await waitFor(() => expect(chatAccess.rename).toHaveBeenLastCalledWith('k1', ''))
    await waitFor(() => expect(titleEl()).toHaveTextContent('proj'))
    expect(chatAccess.record).toHaveBeenCalled() // the derived name is read again
  })

  it('shows a name the person typed in a folder chat when the record says it was renamed', async () => {
    chatAccess.record.mockResolvedValue({ access: 'coding', cwd: '/home/u/proj', title: 'meta challenge', renamed: true })
    await open({ id: 'k1', runtimeId: 'claude', model: 'opus', cwd: '/home/u/proj' })
    await waitFor(() => expect(titleEl()).toHaveTextContent('meta challenge'))
  })

  it('a chat that has not started has no name to change', async () => {
    await open({ draft: true })
    expect(within(overlay()).getByTestId('chat-title')).toHaveTextContent('New chat')
    fireEvent.doubleClick(within(overlay()).getByTestId('chat-title'))
    expect(screen.queryByLabelText('Chat name')).toBeNull()
  })
})
