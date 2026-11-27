// @vitest-environment jsdom
// Card turns are listed apart from the chat's turns and come with their events:
// a chat with many cards costs one read, and the cards stand where they were
// written.
import { describe, it, expect, vi, afterEach } from 'vitest'
import { renderHook, act, cleanup, waitFor } from '@testing-library/react'

const { api, loadTurnState } = vi.hoisted(() => ({ api: { getChatTurns: vi.fn(), getChatCardTurns: vi.fn() }, loadTurnState: vi.fn(async (_c, turn) => ({ calls: {}, parts: [], id: turn.id })) }))
vi.mock('../../services/api.js', () => ({ api, notify: vi.fn(), onChatEvent: () => () => {} }))
vi.mock('../chat/useChatStream.js', () => ({ useChatStream: () => ({ scope: null, calls: {}, parts: [], notices: [] }), loadTurnState }))
import '../../i18n.js'
import { useCoderConversation } from './useCoderConversation.js'

afterEach(() => { cleanup(); vi.clearAllMocks() })
const turn = (id, at, prompt = '') => ({ id, prompt, status: 'completed', createdAt: `2026-11-27T10:00:${at}Z` })
const card = (id, at, surfaceId = 'hil-1') => ({ ...turn(id, at), events: [{ conversationId: 'c1', turnId: id, seq: 1, at: `2026-11-27T10:00:${at}Z`, type: 'ui.surface',
  payload: { surfaceId, provenance: 'system', message: { version: '1.0', createSurface: { surfaceId, components: [] } } } }] })

describe('a conversation with cards', () => {
  it('asks for both and merges them in the order they were written', async () => {
    api.getChatTurns.mockResolvedValue({ items: [turn('t2', '30', 'second'), turn('t1', '10', 'first')] })
    api.getChatCardTurns.mockResolvedValue({ items: [card('system-uiaction-c', '40'), card('system-uisurface-b', '20'), card('system-uisurface-a', '05')] })
    const { result } = renderHook(() => useCoderConversation({ conversationId: 'c1' }))
    await waitFor(() => expect(result.current.messages).toHaveLength(7))
    expect(result.current.messages.map(m => m.turnId || m.content)).toEqual(['system-uisurface-a', 'first', 't1', 'system-uisurface-b', 'second', 't2', 'system-uiaction-c'])
    expect(api.getChatTurns).toHaveBeenCalledWith('c1', '', 50)
    expect(api.getChatCardTurns).toHaveBeenCalledWith('c1')
    expect(result.current.messages[0].state.surfaces[0].surfaceId).toBe('hil-1')
  })
  it('reads no card turn by itself, however many there are, and asks again only for new ones', async () => {
    api.getChatTurns.mockResolvedValue({ items: [turn('t1', '10', 'first')] })
    const many = Array.from({ length: 200 }, (_, i) => card(`system-uisurface-${i}`, '20', `hil-${i}`))
    api.getChatCardTurns.mockResolvedValue({ items: many })
    const { result } = renderHook(() => useCoderConversation({ conversationId: 'c1' }))
    await waitFor(() => expect(result.current.messages).toHaveLength(202))
    expect(loadTurnState).toHaveBeenCalledTimes(1)
    api.getChatCardTurns.mockClear()
    api.getChatCardTurns.mockResolvedValue({ items: [card('system-uiaction-new', '50')] })
    await act(async () => { await result.current.refreshUI() })
    expect(api.getChatCardTurns).toHaveBeenCalledTimes(1)
    const [, seen] = api.getChatCardTurns.mock.calls[0]
    expect(seen).toHaveLength(200)
    expect(loadTurnState).toHaveBeenCalledTimes(1)
    expect(result.current.messages.at(-1).turnId).toBe('system-uiaction-new')
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
