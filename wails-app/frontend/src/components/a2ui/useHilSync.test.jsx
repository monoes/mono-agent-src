// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook, act } from '@testing-library/react'

const { api } = vi.hoisted(() => ({ api: { chatUISyncHIL: vi.fn() } }))
vi.mock('../../services/api.js', () => ({ api, notify: vi.fn() }))
import { useHilSync, HIL_SYNC_MS } from './useHilSync.js'

beforeEach(() => { vi.useFakeTimers(); api.chatUISyncHIL.mockReset() })
afterEach(() => { vi.useRealTimers() })

describe('useHilSync', () => {
  const onChanged = vi.fn()
  it('reads the cards only when the CLI added or closed one', async () => {
    onChanged.mockClear()
    api.chatUISyncHIL.mockResolvedValueOnce({ surfaced: [], resolved: [] }).mockResolvedValueOnce({ surfaced: ['hil-1'], resolved: [] }).mockResolvedValue({ surfaced: [], resolved: ['hil-1'] })
    renderHook(() => useHilSync('c1', onChanged))
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    expect(onChanged).not.toHaveBeenCalled()
    await act(async () => { await vi.advanceTimersByTimeAsync(HIL_SYNC_MS) })
    await act(async () => { await vi.advanceTimersByTimeAsync(HIL_SYNC_MS) })
    expect(onChanged).toHaveBeenCalledTimes(2)
  })
  it('syncs on open then every 10s, and stops on unmount', async () => {
    api.chatUISyncHIL.mockResolvedValue({})
    const { unmount } = renderHook(() => useHilSync('c1', onChanged))
    expect(api.chatUISyncHIL).toHaveBeenCalledTimes(1)
    await act(async () => { await vi.advanceTimersByTimeAsync(HIL_SYNC_MS) })
    expect(api.chatUISyncHIL).toHaveBeenCalledTimes(2)
    unmount()
    await act(async () => { await vi.advanceTimersByTimeAsync(HIL_SYNC_MS * 3) })
    expect(api.chatUISyncHIL).toHaveBeenCalledTimes(2)
  })
  it('never overlaps calls', async () => {
    let release
    api.chatUISyncHIL.mockImplementation(() => new Promise(r => { release = r }))
    renderHook(() => useHilSync('c1', onChanged))
    await act(async () => { await vi.advanceTimersByTimeAsync(HIL_SYNC_MS * 3) })
    expect(api.chatUISyncHIL).toHaveBeenCalledTimes(1)
    await act(async () => { release({}); await vi.advanceTimersByTimeAsync(HIL_SYNC_MS) })
    expect(api.chatUISyncHIL).toHaveBeenCalledTimes(2)
  })
  it('skips while hidden or blurred, warns on failure, and does nothing without a conversation', async () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    api.chatUISyncHIL.mockRejectedValue(new Error('boom'))
    renderHook(() => useHilSync('c1', onChanged))
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    expect(warn).toHaveBeenCalled()
    act(() => { window.dispatchEvent(new Event('blur')) })
    await act(async () => { await vi.advanceTimersByTimeAsync(HIL_SYNC_MS * 2) })
    expect(api.chatUISyncHIL).toHaveBeenCalledTimes(1)
    act(() => { window.dispatchEvent(new Event('focus')) })
    await act(async () => { await vi.advanceTimersByTimeAsync(HIL_SYNC_MS) })
    expect(api.chatUISyncHIL).toHaveBeenCalledTimes(2)
    api.chatUISyncHIL.mockClear()
    renderHook(() => useHilSync('', onChanged))
    expect(api.chatUISyncHIL).not.toHaveBeenCalled()
    warn.mockRestore()
  })
})
