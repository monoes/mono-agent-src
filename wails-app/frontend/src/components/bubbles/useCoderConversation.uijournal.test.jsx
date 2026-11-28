// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest'
import { renderHook, act, cleanup, waitFor } from '@testing-library/react'

const { api, loadTurnState } = vi.hoisted(() => ({ api: { getChatTurns: vi.fn(), getChatCardTurns: vi.fn(() => Promise.resolve({ items: [] })) }, loadTurnState: vi.fn(async (_c, turn) => ({ calls: {}, parts: [], id: turn.id })) }))
vi.mock('../../services/api.js', () => ({ api, notify: vi.fn(), onChatEvent: () => () => {} }))
vi.mock('../chat/useChatStream.js', () => ({ useChatStream: () => ({ scope: null, calls: {}, parts: [], notices: [] }), loadTurnState }))
import '../../i18n.js'
import { useCoderConversation } from './useCoderConversation.js'

afterEach(() => { cleanup(); vi.clearAllMocks() })
const turn = id => ({ id, prompt: '', status: 'completed' })

describe('refreshUI', () => {
  it('adds only the journal turns not shown yet', async () => {
    api.getChatTurns.mockResolvedValue({ items: [turn('t1')] })
    const { result } = renderHook(() => useCoderConversation({ conversationId: 'c1' }))
    await waitFor(() => expect(result.current.messages).toHaveLength(1))
    api.getChatCardTurns.mockResolvedValue({ items: [turn('system-uiaction-2'), turn('system-uisurface-1'), turn('t-other')] })
    await act(async () => { await result.current.refreshUI() })
    expect(result.current.messages.map(m => m.turnId)).toEqual(['t1', 'system-uisurface-1', 'system-uiaction-2'])
    await act(async () => { await result.current.refreshUI() })
    expect(result.current.messages).toHaveLength(3)
  })
  it('only warns when the history cannot be read', async () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    api.getChatTurns.mockResolvedValue({ items: [] })
    const { result } = renderHook(() => useCoderConversation({ conversationId: 'c1' }))
    api.getChatCardTurns.mockRejectedValue(new Error('boom'))
    await act(async () => { await result.current.refreshUI() })
    expect(warn).toHaveBeenCalled()
    warn.mockRestore()
  })
})
