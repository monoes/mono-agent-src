// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest'
import { renderHook, act, waitFor, cleanup } from '@testing-library/react'
import '../../i18n.js'

const { api } = vi.hoisted(() => ({ api: { getChatTurns: vi.fn(), getChatEvents: vi.fn(), startChatTurn: vi.fn(), stopChatTurn: vi.fn() } }))
vi.mock('../../services/api.js', () => ({ api, notify: vi.fn(), onChatEvent: () => () => {} }))

import { useCoderConversation } from './useCoderConversation.js'

afterEach(() => { cleanup(); vi.clearAllMocks() })

const ev = (turnId, seq, type, payload) => ({ version: 1, conversationId: 'c1', turnId, seq, at: '2026-11-06T10:00:00.000Z', type, payload })

describe('a Stop message in the transcript', () => {
  it('a promptless system turn loads as its notice alone, with no empty user message', async () => {
    api.getChatTurns.mockResolvedValue({ items: [
      { id: 'system-ab', prompt: '', status: 'completed' },
      { id: 't1', prompt: 'hello', status: 'completed' },
    ] })
    api.getChatEvents.mockImplementation(async (_c, turnId) => ({ hasMore: false, items: turnId === 't1'
      ? [ev('t1', 1, 'turn.finished', { status: 'completed', historySaved: true })]
      : [ev(turnId, 1, 'notice', { code: 'doomsday.stopped', message: 'Open-ended run stopped by you at T: stopped', severity: 'info' }),
        ev(turnId, 2, 'turn.finished', { status: 'completed', historySaved: true })] }))
    const { result } = renderHook(() => useCoderConversation({ conversationId: 'c1' }))
    await waitFor(() => expect(result.current.loading).toBe(false))
    const roles = result.current.messages.map(m => m.role + (m.role === 'user' ? `:${m.content}` : ''))
    expect(roles).toEqual(['user:hello', 'turn', 'turn']) // oldest first: hello, its answer, then the system notice
    expect(result.current.messages[2].state.notices[0].message).toBe('Open-ended run stopped by you at T: stopped')
  })

  it('addSystemNotice shows the same notice at once', async () => {
    api.getChatTurns.mockResolvedValue({ items: [] })
    const { result } = renderHook(() => useCoderConversation({ conversationId: 'c1' }))
    await waitFor(() => expect(result.current.loading).toBe(false))
    act(() => result.current.addSystemNotice('Open-ended run stopped by you at T: stopped'))
    const last = result.current.messages[result.current.messages.length - 1]
    expect(last.role).toBe('turn')
    expect(last.state.notices).toEqual([{ code: 'doomsday.stopped', message: 'Open-ended run stopped by you at T: stopped', severity: 'info' }])
  })
})
