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
