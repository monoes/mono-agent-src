// @vitest-environment jsdom
import { beforeEach, afterEach, describe, it, expect, vi } from 'vitest'
import { renderHook, act, waitFor, cleanup } from '@testing-library/react'
import '../../i18n.js'

const { api, listeners } = vi.hoisted(() => ({
 api: { getChatTurns: vi.fn(), getChatEvents: vi.fn(), startChatTurn: vi.fn(), stopChatTurn: vi.fn() },
 listeners: new Set(),
}))
vi.mock('../../services/api.js', () => ({ api, notify: vi.fn(), onChatEvent: cb => { listeners.add(cb); return () => listeners.delete(cb) } }))
import { useCoderConversation } from './useCoderConversation.js'
const event = (conversationId, turnId, seq, type, payload) => ({ conversationId, turnId, seq, type, at: '2026-10-25T12:00:00Z', payload })
const emit = async ev => act(async () => { for (const cb of listeners) cb(ev) })
beforeEach(() => { vi.clearAllMocks(); api.getChatTurns.mockResolvedValue({ items: [] }); api.getChatEvents.mockResolvedValue({ items: [], hasMore: false }) })
afterEach(() => { cleanup(); listeners.clear() })

describe('mission continuation turns', () => {
 it('adopts a lead started by approval, without sending a duplicate or adopting another conversation', async () => {
  const { result } = renderHook(() => useCoderConversation({ conversationId: 'bound', create: vi.fn() }))
  await waitFor(() => expect(result.current.loading).toBe(false))
  await emit(event('other', 'wrong', 1, 'turn.started', { text: 'other' }))
  expect(result.current.activeTurnId).toBe('')
  await emit(event('bound', 'mission-7-2', 1, 'turn.started', { text: 'Continue approved session #7' }))
  await waitFor(() => expect(result.current.activeTurnId).toBe('mission-7-2'))
  expect(result.current.messages.at(-1)).toMatchObject({ role: 'user', content: 'Continue approved session #7' })
  expect(api.startChatTurn).not.toHaveBeenCalled()
  await emit(event('bound', 'mission-7-2', 2, 'turn.finished', { status: 'completed' }))
  await waitFor(() => expect(result.current.streaming).toBe(false))
 })
 it('keeps a queued continuation when planning finishes in the same event batch', async () => {
  const { result } = renderHook(() => useCoderConversation({ conversationId: 'bound', create: vi.fn() }))
  await waitFor(() => expect(result.current.loading).toBe(false))
  await emit(event('bound', 'plan', 1, 'turn.started', { text: 'plan' }))
  await waitFor(() => expect(result.current.activeTurnId).toBe('plan'))
  await act(async () => {
   for (const cb of listeners) cb(event('bound', 'plan', 2, 'turn.finished', { status: 'completed' }))
   for (const cb of listeners) cb(event('bound', 'execute', 1, 'turn.started', { text: 'execute approved plan' }))
  })
  await waitFor(() => expect(result.current.activeTurnId).toBe('execute'))
  expect(result.current.streaming).toBe(true)
  expect(result.current.messages.some(m => m.role === 'turn' && m.turnId === 'plan')).toBe(true)
  expect(api.startChatTurn).not.toHaveBeenCalled()
 })
})
