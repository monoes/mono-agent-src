// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook, act, waitFor, cleanup } from '@testing-library/react'
import '../../i18n.js'

const { api } = vi.hoisted(() => ({
  api: { getChatTurns: vi.fn(), getChatEvents: vi.fn(), startChatTurn: vi.fn(), stopChatTurn: vi.fn() },
}))
vi.mock('../../services/api.js', () => ({ api, notify: vi.fn(), onChatEvent: () => () => {} }))

import { useCoderConversation } from './useCoderConversation.js'

beforeEach(() => {
  api.startChatTurn.mockResolvedValue({ ok: true, status: 'active' })
  api.getChatTurns.mockResolvedValue({ items: [] })
  api.getChatEvents.mockResolvedValue({ items: [], hasMore: false })
})
afterEach(() => { cleanup(); vi.clearAllMocks() })

describe('useCoderConversation reset', () => {
  it('empties an idle chat so the next send creates a new conversation', async () => {
    const create = vi.fn().mockResolvedValue({ id: 'c2' })
    const { result } = renderHook(() => useCoderConversation({ conversationId: 'c1', create }))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.conversationId).toBe('c1')
    let ok
    act(() => { ok = result.current.reset() })
    expect(ok).toBe(true)
    expect(result.current.conversationId).toBe('')
    expect(result.current.messages).toEqual([])
    await act(async () => { await result.current.send('hello') })
    expect(create).toHaveBeenCalledTimes(1)
    expect(result.current.conversationId).toBe('c2')
  })

  it('refuses while a turn is running and keeps the conversation', async () => {
    const create = vi.fn().mockResolvedValue({ id: 'c1' })
    const { result } = renderHook(() => useCoderConversation({ conversationId: '', create }))
    await act(async () => { await result.current.send('first') })
    expect(result.current.streaming).toBe(true)
    let ok
    act(() => { ok = result.current.reset() })
    expect(ok).toBe(false)
    expect(result.current.conversationId).toBe('c1')
  })
})
