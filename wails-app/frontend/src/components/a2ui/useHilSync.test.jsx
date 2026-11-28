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
  // Another window or the side panel may have drawn a card again: this sync then
  // finds nothing to do, and only the journal says there is a newer card.
  it('reads the journal after every sync, whatever the sync did or if it failed', async () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    onChanged.mockClear()
    api.chatUISyncHIL.mockResolvedValueOnce({ surfaced: [], resolved: [] }).mockRejectedValueOnce(new Error('busy')).mockResolvedValue({})
    renderHook(() => useHilSync('c1', onChanged))
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    expect(onChanged).toHaveBeenCalledTimes(1)
    await act(async () => { await vi.advanceTimersByTimeAsync(HIL_SYNC_MS) })
    await act(async () => { await vi.advanceTimersByTimeAsync(HIL_SYNC_MS) })
    expect(onChanged).toHaveBeenCalledTimes(3)
    warn.mockRestore()
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
