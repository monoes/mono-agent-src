// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook, act, cleanup } from '@testing-library/react'

const { api } = vi.hoisted(() => ({ api: { list: vi.fn(), approve: vi.fn(), show: vi.fn(), deny: vi.fn(), ask: vi.fn() } }))
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

  it('a refused approval of a moved plan reads the list again', async () => {
    api.list.mockResolvedValue({ items: [{ ...item(1), rev: 4, digest: 'dd' }] })
    const { result } = await mount()
    api.list.mockClear()
    api.approve.mockResolvedValueOnce({ error: 'changed', code: 'plan_changed' })
    let res
    await act(async () => { res = await result.current.answer('approve', { ...item(1), rev: 4, digest: 'dd' }) })
    expect(res.code).toBe('plan_changed')
    expect(api.list).toHaveBeenCalled()
  })

  it('answer runs the verb for the session and reads the list again', async () => {
    api.list.mockResolvedValue({ items: [{ ...item(1), rev: 4, digest: 'dd' }] })
    const { result } = await mount()
    api.list.mockClear()
    await act(async () => { await result.current.answer('deny', item(1), 'no') })
    expect(api.deny).toHaveBeenCalledWith(10, 'no')
    await act(async () => { await result.current.answer('approve', { ...item(1), rev: 4, digest: 'dd' }) })
    expect(api.approve).toHaveBeenCalledWith(10, 4, 'dd')
    expect(api.list).toHaveBeenCalled()
    const bad = await result.current.answer('nope', item(1))
    expect(bad.error).toBeTruthy()
  })

  // The guard the person was shown stays with the card they act on: a poll that brings a newer
  // plan must not swap it, or the approval would cover words they never read.
  describe('the frozen plan guard', () => {
    const plan = (rev, digest, title) => ({ ...item(1), rev, digest, tasks: [{ id: 5, title }] })

    it('never approves with a newer revision and digest than the ones shown', async () => {
      api.list.mockResolvedValue({ items: [plan(4, 'old', 'port the importer')] })
      const { result } = await mount()
      api.list.mockResolvedValue({ items: [plan(5, 'new', 'send the payroll file out')] })
      await act(async () => { await result.current.refresh() })
      // the card re-renders with whatever the hook now holds, then the person clicks
      await act(async () => { await result.current.answer('approve', result.current.items[0]) })
      expect(api.approve).not.toHaveBeenCalledWith(10, 5, 'new')
    })

    it('a changed plan is refused on the client: nothing reaches the server with the old guard either', async () => {
      api.list.mockResolvedValue({ items: [plan(4, 'old', 'port the importer')] })
      const { result } = await mount()
      api.list.mockResolvedValue({ items: [plan(5, 'new', 'send the payroll file out')] })
      await act(async () => { await result.current.refresh() })
      const res = await result.current.answer('approve', result.current.items[0])
      expect(res.code).toBe('plan_changed')
      expect(api.approve).not.toHaveBeenCalled()
    })

    it('keeps the shown content, marks the item changed and refuses to approve until reviewed', async () => {
      api.list.mockResolvedValue({ items: [plan(4, 'old', 'port the importer')] })
      const { result } = await mount()
      api.list.mockResolvedValue({ items: [plan(5, 'new', 'send the payroll file out')] })
      await act(async () => { await result.current.refresh() })
      const it = result.current.items[0]
      expect(it.changed).toBe(true)
      expect(it.tasks[0].title).toBe('port the importer')
      api.approve.mockClear()
      const res = await result.current.answer('approve', it)
      expect(res.code).toBe('plan_changed')
      expect(api.approve).not.toHaveBeenCalled()
    })

    it('review freezes the new pair and its content, and approve then carries it', async () => {
      api.list.mockResolvedValue({ items: [plan(4, 'old', 'port the importer')] })
      const { result } = await mount()
      api.list.mockResolvedValue({ items: [plan(5, 'new', 'send the payroll file out')] })
      await act(async () => { await result.current.refresh() })
      act(() => { result.current.review(1) })
      const it = result.current.items[0]
      expect(it.changed).toBe(false)
      expect(it.tasks[0].title).toBe('send the payroll file out')
      await act(async () => { await result.current.answer('approve', it) })
      expect(api.approve).toHaveBeenCalledWith(10, 5, 'new')
    })
  })

  describe('long plan text', () => {
    const cutItem = { ...item(1), rev: 3, digest: 'D1', tasks: [{ id: 101, title: 't', notes: 'cut…', truncated: true }] }
    const fullItem = { ...cutItem, tasks: [{ id: 101, title: 't', notes: 'whole text' }] }

    it('refuses to approve while a field is cut, and says why', async () => {
      api.list.mockResolvedValue({ items: [cutItem] })
      const { result } = await mount()
      let res
      await act(async () => { res = await result.current.answer('approve', result.current.items[0]) })
      expect(res.code).toBe('truncated')
      expect(api.approve).not.toHaveBeenCalled()
    })

    it('approves after expand, with the very rev and digest that were shown, and keeps the text over a poll', async () => {
      api.list.mockResolvedValue({ items: [cutItem] })
      api.show.mockResolvedValue({ item: fullItem })
      const { result } = await mount()
      await act(async () => { await result.current.expand(result.current.items[0]) })
      expect(api.show).toHaveBeenCalledWith(10)
      expect(result.current.items[0].tasks[0].notes).toBe('whole text')
      await act(async () => { await result.current.refresh() })
      expect(result.current.items[0].tasks[0].notes).toBe('whole text')
      await act(async () => { await result.current.answer('approve', result.current.items[0]) })
      expect(api.approve).toHaveBeenCalledWith(10, 3, 'D1')
    })

    it('treats a full text under another digest as a changed plan and does not adopt it', async () => {
      api.list.mockResolvedValue({ items: [cutItem] })
      api.show.mockResolvedValue({ item: { ...fullItem, digest: 'D2' } })
      const { result } = await mount()
      await act(async () => { await result.current.expand(result.current.items[0]) })
      const it = result.current.items[0]
      expect(it.changed).toBe(true)
      expect(it.tasks[0].truncated).toBe(true)
      let res
      await act(async () => { res = await result.current.answer('approve', it) })
      expect(res.code).toBe('plan_changed')
      expect(api.approve).not.toHaveBeenCalled()
    })
  })

  it('binds approve to the frozen item\'s own session and refuses another one', async () => {
    api.list.mockResolvedValue({ items: [{ ...item(1), rev: 4, digest: 'same' }] })
    const { result } = await mount()
    let res
    await act(async () => { res = await result.current.answer('approve', { ...result.current.items[0], sessionId: 99 }) })
    expect(res.code).toBe('session_mismatch')
    expect(api.approve).not.toHaveBeenCalled()
    await act(async () => { await result.current.answer('approve', result.current.items[0]) })
    expect(api.approve).toHaveBeenCalledWith(10, 4, 'same')
  })
})
