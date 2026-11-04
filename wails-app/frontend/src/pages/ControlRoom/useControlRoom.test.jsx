// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook, act, cleanup } from '@testing-library/react'
import { useControlRoom } from './useControlRoom.js'

const ok = { schema_version: 1, available: true, run: { run_id: 'r', active: true }, spend: { used_micro: 0 }, integrity: { chain_verified: 'yes' } }
let api
const setHidden = (h) => {
  Object.defineProperty(document, 'hidden', { configurable: true, get: () => h })
  document.dispatchEvent(new Event('visibilitychange'))
}
beforeEach(() => {
  vi.useFakeTimers()
  setHidden(false)
  api = { overview: vi.fn().mockResolvedValue(ok), feed: vi.fn().mockResolvedValue({ items: [{ seq: 2 }, { seq: 1 }], next_before: 0 }) }
})
afterEach(() => { cleanup(); vi.useRealTimers(); setHidden(false) })

const tick = (ms) => act(async () => { await vi.advanceTimersByTimeAsync(ms) })

describe('useControlRoom polling', () => {
  it('polls no faster than every 3 s', async () => {
    renderHook(() => useControlRoom(true, api))
    await tick(0)
    expect(api.overview).toHaveBeenCalledTimes(1)
    await tick(2900)
    expect(api.overview).toHaveBeenCalledTimes(1)
    await tick(200)
    expect(api.overview).toHaveBeenCalledTimes(2)
    await tick(10_000)
    expect(api.overview.mock.calls.length).toBeLessThanOrEqual(2 + 4)
  })
  it('backs off after errors and recovers', async () => {
    api.overview.mockResolvedValue({ error: 'boom' })
    renderHook(() => useControlRoom(true, api))
    await tick(0)            // 1st
    await tick(6000)         // 2nd after 6 s (2 x 3 s)
    expect(api.overview).toHaveBeenCalledTimes(2)
    await tick(11_000)       // next after 12 s: not yet
    expect(api.overview).toHaveBeenCalledTimes(2)
    await tick(2000)
    expect(api.overview).toHaveBeenCalledTimes(3)
  })
  it('does not poll when the page is not active or the window is hidden, and resumes', async () => {
    const { rerender } = renderHook(({ active }) => useControlRoom(active, api), { initialProps: { active: false } })
    await tick(20_000)
    expect(api.overview).not.toHaveBeenCalled()
    rerender({ active: true })
    await tick(0)
    expect(api.overview).toHaveBeenCalledTimes(1)
    act(() => setHidden(true))
    const n = api.overview.mock.calls.length
    await tick(60_000)
    expect(api.overview).toHaveBeenCalledTimes(n)
    act(() => setHidden(false))
    await tick(0)
    expect(api.overview.mock.calls.length).toBe(n + 1)
  })
  it('asks for the feed only when the chain verified, and never shows rows from a bad chain', async () => {
    api.overview.mockResolvedValue({ ...ok, integrity: { chain_verified: 'no' } })
    const { result } = renderHook(() => useControlRoom(true, api))
    await tick(0)
    expect(api.feed).not.toHaveBeenCalled()
    expect(result.current.feed.items).toEqual([])
    api.overview.mockResolvedValue(ok)
    await tick(3100)
    expect(api.feed).toHaveBeenCalledTimes(1)
    expect(result.current.feed.items.map(r => r.seq)).toEqual([2, 1])
    api.overview.mockResolvedValue({ ...ok, integrity: { chain_verified: 'unknown' } })
    await tick(3100)
    expect(result.current.feed.items).toEqual([])
  })
  it('an unanchored chain still shows its feed, and an error document keeps the last known run', async () => {
    api.overview.mockResolvedValue({ ...ok, integrity: { chain_verified: 'unanchored', reason: 'no_witness' } })
    const { result } = renderHook(() => useControlRoom(true, api))
    await tick(0)
    expect(api.feed).toHaveBeenCalledTimes(1)
    expect(result.current.feed.items.map(r => r.seq)).toEqual([2, 1])
    api.overview.mockResolvedValue({ schema_version: 1, available: false, reason: 'error', error_code: 'read_failed' })
    await tick(3100)
    expect(result.current.view).toMatchObject({ state: 'error', errorCode: 'read_failed' })
    expect(result.current.lastRun.run.run_id).toBe('r')
  })
  it.each(['no', 'unknown'])('a feed page that reports integrity %s drops the rows already shown and says so', async (chain) => {
    const { result } = renderHook(() => useControlRoom(true, api))
    await tick(0)
    expect(result.current.feed.items.map(r => r.seq)).toEqual([2, 1])
    api.feed.mockResolvedValue({ schema_version: 1, available: true, integrity: { chain_verified: chain, reason: 'x' }, items: [], next_before: 0 })
    await tick(3100)
    expect(result.current.feed.items).toEqual([])
    expect(result.current.feed.bad).toMatchObject({ chain: expect.any(String) })
  })
  it('older rows from a feed page that fails its integrity are dropped too', async () => {
    api.feed.mockResolvedValue({ items: [{ seq: 5 }, { seq: 4 }], next_before: 4, integrity: { chain_verified: 'yes' } })
    const { result } = renderHook(() => useControlRoom(true, api))
    await tick(0)
    api.feed.mockResolvedValue({ items: [{ seq: 3 }], next_before: 0, integrity: { chain_verified: 'no' } })
    await act(async () => { await result.current.loadOlder() })
    expect(result.current.feed.items).toEqual([])
  })
  it('stops when unmounted', async () => {
    const { unmount } = renderHook(() => useControlRoom(true, api))
    await tick(0)
    unmount()
    const n = api.overview.mock.calls.length
    await tick(30_000)
    expect(api.overview).toHaveBeenCalledTimes(n)
  })
  it('estimates a rate only from successive polls', async () => {
    let used = 0
    api.overview.mockImplementation(async () => ({ ...ok, spend: { used_micro: used } }))
    const { result } = renderHook(() => useControlRoom(true, api))
    await tick(0)
    expect(result.current.rate).toBeNull()
    used = 1000
    await tick(31_000)
    expect(result.current.rate).toBeGreaterThan(0)
  })
})
