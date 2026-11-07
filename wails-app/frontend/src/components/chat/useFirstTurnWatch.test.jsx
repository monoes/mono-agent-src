// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest'
import { renderHook, act, cleanup } from '@testing-library/react'
import { useFirstTurnWatch, FIRST_TURN_FIRST_MS, FIRST_TURN_POLL_MS, FIRST_TURN_POLLS } from './useFirstTurnWatch.js'

beforeEach(() => vi.useFakeTimers())
afterEach(() => { cleanup(); vi.useRealTimers() })

const tick = ms => act(async () => { await vi.advanceTimersByTimeAsync(ms) })

describe('watching the boss\'s first turn after the start', () => {
  it('reports a first turn the host refused, with its reason, once pending is over', async () => {
    const status = vi.fn()
      .mockResolvedValueOnce({ active: true, first_turn: 'pending' })
      .mockResolvedValue({ active: true, first_turn: 'failed', first_turn_error: 'no run without binding: off' })
    const onFailed = vi.fn(), onSettled = vi.fn()
    renderHook(() => useFirstTurnWatch(true, onFailed, onSettled, { status }))
    await tick(FIRST_TURN_FIRST_MS)
    expect(onFailed).not.toHaveBeenCalled()
    await tick(FIRST_TURN_POLL_MS)
    expect(onFailed).toHaveBeenCalledWith('no run without binding: off')
    expect(onSettled).not.toHaveBeenCalled()
    await tick(10 * FIRST_TURN_POLL_MS)
    expect(status).toHaveBeenCalledTimes(2) // it stops asking
  })
  it('reports a first turn that was never queued as failed', async () => {
    const onFailed = vi.fn()
    renderHook(() => useFirstTurnWatch(true, onFailed, vi.fn(), { status: async () => ({ active: true, first_turn: 'none' }) }))
    await tick(FIRST_TURN_FIRST_MS)
    expect(onFailed).toHaveBeenCalledWith('')
  })
  it('settles quietly when the turn started or the run is gone, and keeps asking through a failed read', async () => {
    const onFailed = vi.fn(), onSettled = vi.fn()
    const status = vi.fn().mockResolvedValueOnce({ error: 'x' }).mockResolvedValue({ active: true, first_turn: 'started' })
    renderHook(() => useFirstTurnWatch(true, onFailed, onSettled, { status }))
    await tick(FIRST_TURN_FIRST_MS + FIRST_TURN_POLL_MS)
    expect(onSettled).toHaveBeenCalledTimes(1)
    expect(onFailed).not.toHaveBeenCalled()
    const gone = vi.fn()
    renderHook(() => useFirstTurnWatch(true, onFailed, gone, { status: async () => ({ active: false }) }))
    await tick(FIRST_TURN_FIRST_MS)
    expect(gone).toHaveBeenCalledTimes(1)
  })
  it('gives up after a bounded number of polls and asks nothing when not watching or unmounted', async () => {
    const status = vi.fn().mockResolvedValue({ active: true, first_turn: 'pending' })
    const onSettled = vi.fn()
    renderHook(() => useFirstTurnWatch(true, vi.fn(), onSettled, { status }))
    await tick(FIRST_TURN_FIRST_MS + FIRST_TURN_POLLS * FIRST_TURN_POLL_MS)
    expect(status).toHaveBeenCalledTimes(FIRST_TURN_POLLS)
    expect(onSettled).toHaveBeenCalledTimes(1)
    const idle = vi.fn()
    renderHook(() => useFirstTurnWatch(false, vi.fn(), vi.fn(), { status: idle }))
    const gone = vi.fn()
    const h = renderHook(() => useFirstTurnWatch(true, vi.fn(), vi.fn(), { status: gone }))
    h.unmount()
    await tick(5 * FIRST_TURN_POLL_MS)
    expect(idle).not.toHaveBeenCalled()
    expect(gone).not.toHaveBeenCalled()
  })
  it('shares one status read between two watchers asking at the same time', async () => {
    const status = vi.fn().mockResolvedValue({ active: true, first_turn: 'started' })
    const api = { status }
    renderHook(() => useFirstTurnWatch(true, vi.fn(), vi.fn(), api))
    renderHook(() => useFirstTurnWatch(true, vi.fn(), vi.fn(), api))
    await tick(FIRST_TURN_FIRST_MS)
    expect(status).toHaveBeenCalledTimes(1)
  })
})
