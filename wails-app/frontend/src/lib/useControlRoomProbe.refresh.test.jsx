// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { renderHook, waitFor, act } from '@testing-library/react'
import { useControlRoomProbe, CONTROL_ROOM_CHANGED } from './useControlRoomProbe.js'

describe('the nav probe after a start', () => {
  it('asks again as soon as the control room is said to have changed, not at its next idle poll', async () => {
    const api = { probe: vi.fn().mockResolvedValue({ mode_available: true, run_exists: false }) }
    const { result } = renderHook(() => useControlRoomProbe(api))
    await waitFor(() => expect(api.probe).toHaveBeenCalledTimes(1))
    api.probe.mockResolvedValue({ mode_available: true, run_exists: true })
    act(() => { window.dispatchEvent(new Event(CONTROL_ROOM_CHANGED)) })
    await waitFor(() => expect(result.current.running).toBe(true))
    expect(api.probe).toHaveBeenCalledTimes(2)
  })
})

describe('one request chain', () => {
  it('a change announced while a read is in flight does not start a second chain: it asks once more after that read', async () => {
    let release
    const api = { probe: vi.fn() }
    api.probe.mockImplementationOnce(() => new Promise(r => { release = () => r({ mode_available: true, run_exists: false }) }))
    api.probe.mockResolvedValue({ mode_available: true, run_exists: true })
    const { result } = renderHook(() => useControlRoomProbe(api))
    await waitFor(() => expect(api.probe).toHaveBeenCalledTimes(1))
    act(() => { window.dispatchEvent(new Event(CONTROL_ROOM_CHANGED)); window.dispatchEvent(new Event(CONTROL_ROOM_CHANGED)) })
    expect(api.probe).toHaveBeenCalledTimes(1) // nothing overlaps the read in flight
    await act(async () => { release() })
    await waitFor(() => expect(result.current.running).toBe(true))
    expect(api.probe).toHaveBeenCalledTimes(2) // two announcements, one more read
  })
})

describe('a rejected probe', () => {
  it('is a failed read the hook handles itself: the error state is surfaced, nothing escapes, and a later announcement still reads', async () => {
    const api = { probe: vi.fn() }
    api.probe.mockRejectedValueOnce(new Error('bridge gone'))
    api.probe.mockResolvedValue({ mode_available: true, run_exists: true })
    const { result } = renderHook(() => useControlRoomProbe(api))
    await waitFor(() => expect(result.current.failed).toBe(true))
    expect(result.current.show).toBe(false) // nothing good has been read yet
    act(() => { window.dispatchEvent(new Event(CONTROL_ROOM_CHANGED)) })
    await waitFor(() => expect(result.current.running).toBe(true))
    expect(result.current.failed).toBe(false)
  })
  it('keeps the last good answer while a read fails', async () => {
    const api = { probe: vi.fn() }
    api.probe.mockResolvedValueOnce({ mode_available: true, run_exists: true })
    api.probe.mockRejectedValue(new Error('bridge gone'))
    const { result } = renderHook(() => useControlRoomProbe(api))
    await waitFor(() => expect(result.current.running).toBe(true))
    act(() => { window.dispatchEvent(new Event(CONTROL_ROOM_CHANGED)) })
    await waitFor(() => expect(result.current.failed).toBe(true))
    expect(result.current.running).toBe(true)
  })
})
