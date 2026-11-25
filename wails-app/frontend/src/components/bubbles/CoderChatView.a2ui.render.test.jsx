// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup, configure } from '@testing-library/react'
import '../../i18n.js'

configure({ asyncUtilTimeout: 8000 })
vi.setConfig({ testTimeout: 20000 })

const { api, notify } = vi.hoisted(() => ({
  api: { chatUIAction: vi.fn(), chatUISyncHIL: vi.fn(() => Promise.resolve({ surfaced: [], resolved: [] })) },
  notify: vi.fn(),
}))
vi.mock('../../services/api.js', () => ({
  api: new Proxy(api, { get: (t, k) => (k in t ? t[k] : () => Promise.resolve([])) }),
  notify,
  onChatEvent: () => () => {},
}))
vi.mock('../../services/taskSessions.js', () => ({ sessionsApi: { list: () => Promise.resolve({ sessions: [] }) } }))
vi.mock('./useHilAsks.js', () => ({ useHilAsks: () => ({ asks: [], busy: {}, outcome: {}, resolve: vi.fn() }) }))
vi.mock('./PersonItemViews.jsx', () => ({ PersonItemsRegion: () => null }))
vi.mock('./mission/MissionStrip.jsx', () => ({ default: () => null }))

import { CoderChatView } from './CoderChatView.jsx'
import { chatReducer, initialChatState } from '../chat/chatReducer.js'
import { hilApproval, structuredQuestion } from '../a2ui/fixtures.js'

const turnState = (turnId, evs) => evs.reduce((s, [type, payload], i) => chatReducer(s, { type: 'event', event: { conversationId: 'chat-1', turnId, seq: i + 1, at: '2026-11-24T10:00:00Z', type, payload } }),
  chatReducer(initialChatState(), { type: 'scope', scope: { conversationId: 'chat-1', turnId } }))
const done = ['turn.finished', { status: 'completed' }]
const surfaceTurn = () => ({ role: 'turn', turnId: 't1', state: turnState('t1', [['ui.surface', hilApproval[0]], done]) })

const conv = (over = {}) => ({
  conversationId: 'chat-1', messages: [surfaceTurn()], liveTurn: { calls: {}, lastSeq: 0 }, activeTurnId: '', streaming: false, loading: false,
  loadError: '', announcement: '', send: vi.fn(() => Promise.resolve(true)), stop: vi.fn(), model: '', refreshUI: vi.fn(), ...over,
})
const view = c => <CoderChatView conv={c} isDraft={false} access="full" setup={{ workspace: { kind: 'root' }, runtime: '', model: '', effort: '' }} onSetupChange={() => {}} draft="" onDraftChange={() => {}} />

beforeEach(() => { Element.prototype.scrollTo = function scrollTo() {} })
afterEach(() => { cleanup(); vi.clearAllMocks() })

const approve = () => fireEvent.click(screen.getByRole('button', { name: 'Approve and send' }))

describe('CoderChatView A2UI wiring', () => {
  it('syncs HIL on open', async () => {
    render(view(conv()))
    await waitFor(() => expect(api.chatUISyncHIL).toHaveBeenCalledWith('chat-1'))
  })

  it('passes the exact envelope to the binding and sends a next_turn message', async () => {
    api.chatUIAction.mockResolvedValue({ outcome: 'accepted', delivered: 'next_turn', message: 'Approved: 482' })
    const c = conv()
    render(view(c))
    approve()
    await waitFor(() => expect(api.chatUIAction).toHaveBeenCalledTimes(1))
    const [id, env] = api.chatUIAction.mock.calls[0]
    expect(id).toBe('chat-1')
    expect(env).toMatchObject({ version: '1.0', action: { name: 'hil.approve', surfaceId: 'hil-482', sourceComponentId: 'approve', context: { id: '482' } } })
    await waitFor(() => expect(c.send).toHaveBeenCalledWith('Approved: 482', []))
    expect(c.refreshUI).toHaveBeenCalled()
  })

  it('does not double-submit a quick second press', async () => {
    api.chatUIAction.mockResolvedValue({ outcome: 'accepted', delivered: 'answer' })
    const c = conv()
    render(view(c))
    approve(); approve()
    await waitFor(() => expect(api.chatUIAction).toHaveBeenCalledTimes(1))
    expect(c.send).not.toHaveBeenCalled()
  })

  it('shows a rejected reason as a toast and keeps the card live', async () => {
    api.chatUIAction.mockResolvedValue({ outcome: 'rejected', reason: 'not allowed here' })
    const c = conv()
    render(view(c))
    approve()
    await waitFor(() => expect(notify).toHaveBeenCalledWith('a2ui', 'not allowed here'))
    expect(c.send).not.toHaveBeenCalled()
    expect(screen.getByTestId('a2ui-surface')).toHaveAttribute('data-status', 'live')
  })

  it('holds a next_turn message while a turn runs and sends it when the turn ends', async () => {
    api.chatUIAction.mockResolvedValue({ outcome: 'accepted', delivered: 'next_turn', message: 'later' })
    const c = conv({ streaming: true, activeTurnId: 't2', liveTurn: initialChatState() })
    const { rerender } = render(view(c))
    approve()
    await waitFor(() => expect(notify).toHaveBeenCalledWith('a2ui', expect.stringMatching(/queued/)))
    expect(c.send).not.toHaveBeenCalled()
    rerender(view({ ...c, streaming: false, activeTurnId: '' }))
    await waitFor(() => expect(c.send).toHaveBeenCalledWith('later', []))
  })

  it('reads the new cards when a sync added one', async () => {
    api.chatUISyncHIL.mockResolvedValueOnce({ surfaced: ['hil-482'], resolved: [] })
    const c = conv({ messages: [] })
    render(view(c))
    await waitFor(() => expect(c.refreshUI).toHaveBeenCalled())
  })

  it('draws nothing for a press turn, and shows it on the agent card', () => {
    const q = { role: 'turn', turnId: 't1', state: turnState('t1', [['ui.surface', structuredQuestion[0]], done]) }
    const ui = { role: 'turn', turnId: 'system-uiaction-ab', state: turnState('system-uiaction-ab', [['ui.action', { surfaceId: 'ask-17', name: 'answer', outcome: 'accepted' }], done]) }
    const { container } = render(view(conv({ messages: [q, ui] })))
    expect(container.querySelectorAll('.chat-assistant-turn')).toHaveLength(1)
    expect(screen.getByTestId('a2ui-surface')).toHaveAttribute('data-status', 'resolved')
  })

  it('draws a synced card without a turn status, and replaces it in place when decided', () => {
    const synced = { role: 'turn', turnId: 'system-uisurface-1', state: turnState('system-uisurface-1', [['ui.surface', hilApproval[0]], done]) }
    const { container, rerender } = render(view(conv({ messages: [synced] })))
    expect(screen.getByRole('button', { name: 'Reject' })).toBeInTheDocument()
    expect(container.textContent).not.toContain('Completed')
    const msg = m => ({ version: '1.0', ...m })
    const notice = { role: 'turn', turnId: 'system-uisurface-2', state: turnState('system-uisurface-2', [
      ['ui.surface', { surfaceId: 'hil-482', provenance: 'system', message: msg({ deleteSurface: { surfaceId: 'hil-482' } }) }],
      ['ui.surface', { surfaceId: 'hil-482', provenance: 'system', message: msg({ createSurface: { surfaceId: 'hil-482', components: [{ id: 'root', component: 'Badge', text: 'No longer pending', tone: 'neutral' }] } }) }],
      done]) }
    rerender(view(conv({ messages: [synced, notice] })))
    expect(screen.queryByRole('button', { name: 'Reject' })).toBeNull()
    expect(screen.getAllByTestId('a2ui-surface')).toHaveLength(1)
    expect(screen.getByText('No longer pending')).toBeInTheDocument()
  })
})
