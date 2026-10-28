// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook, act, cleanup } from '@testing-library/react'

const { api } = vi.hoisted(() => ({ api: { list: vi.fn(), approve: vi.fn(), deny: vi.fn(), ask: vi.fn() } }))
vi.mock('../../services/personItems.js', () => ({ personItemsApi: api }))

import { usePersonItems } from './usePersonItems.js'
import { setNotifySound } from '../../lib/personChime.js'

const item = id => ({ id, kind: 'plan_approval', sessionId: id * 10, conversationId: `c${id}` })
const listOf = (...ids) => ({ items: ids.map(item) })
let t
const now = () => t
const flush = () => act(async () => { await Promise.resolve() })

beforeEach(() => {
  vi.clearAllMocks()
  localStorage.clear()
  setNotifySound(true)
  t = 1_000_000
  api.list.mockResolvedValue(listOf())
  api.approve.mockResolvedValue({ session: {} })
})
afterEach(() => { cleanup(); delete globalThis.Notification })

async function mount(extra = {}) {
  const play = vi.fn()
  const h = renderHook(() => usePersonItems({ play, now, alerts: { title: 'Needs you', body: n => `${n} waiting` }, ...extra }))
  await flush()
  return { play, ...h }
}

describe('usePersonItems', () => {
  it('chimes once for a new item and not again while it stays open', async () => {
    api.list.mockResolvedValue(listOf(1))
    const { play, result } = await mount()
    expect(result.current.items).toHaveLength(1)
    expect(play).toHaveBeenCalledTimes(1)
    t += 60_000
    await act(async () => { await result.current.refresh() })
    expect(play).toHaveBeenCalledTimes(1)
  })

  it('does not chime again after a restart for an item it already announced', async () => {
    api.list.mockResolvedValue(listOf(1))
    const first = await mount()
    expect(first.play).toHaveBeenCalledTimes(1)
    cleanup()
    const second = await mount()
    expect(second.play).not.toHaveBeenCalled()
  })

  it('debounces a burst: a second new item within five seconds is silent', async () => {
    api.list.mockResolvedValue(listOf(1))
    const { play, result } = await mount()
    api.list.mockResolvedValue(listOf(1, 2))
    t += 2000
    await act(async () => { await result.current.refresh() })
    expect(play).toHaveBeenCalledTimes(1)
    api.list.mockResolvedValue(listOf(1, 2, 3))
    t += 6000
    await act(async () => { await result.current.refresh() })
    expect(play).toHaveBeenCalledTimes(2)
  })

  it('is silent when muted, but the item still shows and the notification still fires', async () => {
    setNotifySound(false)
    const ctor = vi.fn()
    globalThis.Notification = Object.assign(function (...a) { ctor(...a) }, { permission: 'granted' })
    api.list.mockResolvedValue(listOf(1))
    const { play, result } = await mount()
    expect(play).not.toHaveBeenCalled()
    expect(result.current.items).toHaveLength(1)
    expect(ctor).toHaveBeenCalledWith('Needs you', { body: '1 waiting', tag: 'person-item' })
  })

  it('never throws when the audio is blocked', async () => {
    api.list.mockResolvedValue(listOf(1))
    const { result } = await mount({ play: () => { throw new Error('NotAllowedError') } })
    expect(result.current.items).toHaveLength(1)
  })

  it('keeps the last list over a failed read', async () => {
    api.list.mockResolvedValue(listOf(1))
    const { result } = await mount()
    api.list.mockResolvedValue({ error: 'boom' })
    await act(async () => { await result.current.refresh() })
    expect(result.current.items).toHaveLength(1)
  })

  it('answer runs the verb for the session and reads the list again', async () => {
    api.list.mockResolvedValue(listOf(1))
    const { result } = await mount()
    api.list.mockClear()
    await act(async () => { await result.current.answer('deny', item(1), 'no') })
    expect(api.deny).toHaveBeenCalledWith(10, 'no')
    await act(async () => { await result.current.answer('approve', item(1)) })
    expect(api.approve).toHaveBeenCalledWith(10)
    expect(api.list).toHaveBeenCalled()
    const bad = await result.current.answer('nope', item(1))
    expect(bad.error).toBeTruthy()
  })
})
