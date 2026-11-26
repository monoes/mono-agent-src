// @vitest-environment jsdom
// Card turns are listed apart from the chat's turns: a chat with many cards
// still loads its history, and the cards stand where they were written.
import { describe, it, expect, vi, afterEach } from 'vitest'
import { renderHook, cleanup, waitFor } from '@testing-library/react'

const { api, loadTurnState } = vi.hoisted(() => ({ api: { getChatTurns: vi.fn(), getChatCardTurns: vi.fn() }, loadTurnState: vi.fn(async (_c, turn) => ({ calls: {}, parts: [], id: turn.id })) }))
vi.mock('../../services/api.js', () => ({ api, notify: vi.fn(), onChatEvent: () => () => {} }))
vi.mock('../chat/useChatStream.js', () => ({ useChatStream: () => ({ scope: null, calls: {}, parts: [], notices: [] }), loadTurnState }))
import '../../i18n.js'
import { useCoderConversation } from './useCoderConversation.js'

afterEach(() => { cleanup(); vi.clearAllMocks() })
const turn = (id, at, prompt = '') => ({ id, prompt, status: 'completed', createdAt: `2026-11-27T10:00:${at}Z` })

describe('a conversation with cards', () => {
  it('asks for both and merges them in the order they were written', async () => {
    api.getChatTurns.mockResolvedValue({ items: [turn('t2', '30', 'second'), turn('t1', '10', 'first')] })
    api.getChatCardTurns.mockResolvedValue({ items: [turn('system-uiaction-c', '40'), turn('system-uisurface-b', '20'), turn('system-uisurface-a', '05')] })
    const { result } = renderHook(() => useCoderConversation({ conversationId: 'c1' }))
    await waitFor(() => expect(result.current.messages).toHaveLength(7))
    expect(result.current.messages.map(m => m.turnId || m.content)).toEqual(['system-uisurface-a', 'first', 't1', 'system-uisurface-b', 'second', 't2', 'system-uiaction-c'])
    expect(api.getChatTurns).toHaveBeenCalledWith('c1', '', 50)
    expect(api.getChatCardTurns).toHaveBeenCalledWith('c1')
  })
  it('shows the history when the cards cannot be read', async () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    api.getChatTurns.mockResolvedValue({ items: [turn('t1', '10', 'first')] })
    api.getChatCardTurns.mockRejectedValue(new Error('boom'))
    const { result } = renderHook(() => useCoderConversation({ conversationId: 'c1' }))
    await waitFor(() => expect(result.current.messages).toHaveLength(2))
    expect(result.current.loadError).toBe('')
    expect(warn).toHaveBeenCalled()
    warn.mockRestore()
  })
})
