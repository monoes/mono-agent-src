// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook, act, cleanup } from '@testing-library/react'

const { api } = vi.hoisted(() => ({ api: { list: vi.fn(), approve: vi.fn(), deny: vi.fn(), ask: vi.fn(), answerAsk: vi.fn() } }))
vi.mock('../../services/personItems.js', () => ({ personItemsApi: api }))

import { usePersonItems } from './usePersonItems.js'
import { setNotifySound } from '../../lib/personChime.js'

const askItem = { id: 8, kind: 'ancestor_question', askId: 4, sessionId: 31, conversationId: 'c-leaf', question: 'q' }
// stable per-render inputs: a fresh play/now each render would change refresh and loop
const play = vi.fn()
const now = () => 1_000_000
const flush = () => act(async () => { await Promise.resolve() })

beforeEach(() => {
  vi.clearAllMocks(); localStorage.clear(); setNotifySound(true)
  api.list.mockResolvedValue({ items: [askItem] })
  api.answerAsk.mockResolvedValue({ status: 'answered' })
})
afterEach(cleanup)

describe('usePersonItems with an ask item', () => {
  it('chimes for a new ask item, once', async () => {
    const { result } = renderHook(() => usePersonItems({ play, now }))
    await flush()
    expect(result.current.items).toHaveLength(1)
    expect(play).toHaveBeenCalledTimes(1)
    await act(async () => { await result.current.refresh() })
    expect(play).toHaveBeenCalledTimes(1)
  })

  it('answerAsk sends the ask id (not the session id) and the words, then reads the list again', async () => {
    const { result } = renderHook(() => usePersonItems({ play, now }))
    await flush()
    api.list.mockClear()
    const res = await act(async () => result.current.answer('answerAsk', askItem, 'yes'))
    expect(api.answerAsk).toHaveBeenCalledWith(4, 'yes')
    expect(api.approve).not.toHaveBeenCalled()
    expect(res).toEqual({ status: 'answered' })
    expect(api.list).toHaveBeenCalled()
  })

  it('does not read the list again after a refusal', async () => {
    api.answerAsk.mockResolvedValue({ error: 'nope' })
    const { result } = renderHook(() => usePersonItems({ play, now }))
    await flush()
    api.list.mockClear()
    await act(async () => { await result.current.answer('answerAsk', askItem, 'yes') })
    expect(api.list).not.toHaveBeenCalled()
  })
})
