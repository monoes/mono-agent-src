// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, act } from '@testing-library/react'
import { renderHook, waitFor } from '@testing-library/react'
import '../i18n.js'

vi.mock('../wailsjs/go/main/App', () => ({
  GetVersion: vi.fn().mockResolvedValue({ version: '1.0.0' }), GetProfiles: vi.fn().mockResolvedValue([]), IsReady: vi.fn().mockResolvedValue(true),
  SwitchProfile: undefined, ChooseProfileFolder: undefined, MoveProfileFolder: undefined, RevealProfileFolder: undefined,
}))
vi.mock('../services/tasks.js', () => ({ tasksApi: { pulse: vi.fn().mockResolvedValue({}) }, onTasksChanged: () => () => {} }))
vi.mock('./account/AccountMenu.jsx', () => ({ default: () => null }))
vi.mock('./NewProfileModal.jsx', () => ({ default: () => null }))

import Sidebar from './Sidebar.jsx'
import { useControlRoomProbe, probeShows } from '../lib/useControlRoomProbe.js'

afterEach(() => { cleanup(); localStorage.clear() })
const nav = (props) => render(<Sidebar activePage="dashboard" onNavigate={() => {}} stats={null} dbConnected {...props} />)

describe('the control room in the nav', () => {
  it('is absent when the probe says unavailable, and says nothing about the mode', () => {
    for (const cr of [undefined, null, { show: false, running: false }, { show: false, running: true }]) {
      const { container, unmount } = nav({ controlRoom: cr })
      expect(screen.queryByRole('button', { name: 'Control room' })).toBeNull()
      expect(container.textContent.toLowerCase()).not.toMatch(/control room|doomsday|run active/)
      unmount()
    }
  })
  it('is present when the probe shows it, with a chip only while a run exists', () => {
    const { unmount } = nav({ controlRoom: { show: true, running: false } })
    expect(screen.getByRole('button', { name: 'Control room' })).toBeInTheDocument()
    expect(screen.queryByTestId('nav-run-active')).toBeNull()
    unmount()
    nav({ controlRoom: { show: true, running: true } })
    expect(screen.getByTestId('nav-run-active')).toHaveTextContent('run active')
  })
})

describe('the probe rule', () => {
  it('shows whenever the mode is available, with or without a run or the ui flag', () => {
    expect(probeShows(null, true)).toBe(false)
    expect(probeShows({ mode_available: false, run_exists: true }, true)).toBe(false)
    expect(probeShows({ mode_available: true, run_exists: false }, false)).toBe(true)
    expect(probeShows({ mode_available: true, run_exists: true }, false)).toBe(true)
    expect(probeShows({ mode_available: true, run_exists: false }, true)).toBe(true)
  })
  it('the hook shows an idle available mode without a chip', async () => {
    const api = { probe: vi.fn().mockResolvedValue({ mode_available: true, run_exists: false }) }
    const { result } = renderHook(() => useControlRoomProbe(api))
    await waitFor(() => expect(result.current).toEqual({ show: true, running: false }))
  })
  it('the hook is hidden on an error and on the unavailable answer, shown on a run', async () => {
    const api = { probe: vi.fn().mockResolvedValue({ error: 'x' }) }
    const { result, rerender } = renderHook(() => useControlRoomProbe(api))
    await waitFor(() => expect(api.probe).toHaveBeenCalled())
    expect(result.current).toEqual({ show: false, running: false })
    api.probe.mockResolvedValue({ mode_available: true, run_exists: true })
    const r2 = renderHook(() => useControlRoomProbe(api))
    await waitFor(() => expect(r2.result.current).toEqual({ show: true, running: true }))
    rerender()
  })
})

describe('the probe keeps its last good answer and paces itself', () => {
  const run = { mode_available: true, run_exists: true }
  const idle = { mode_available: true, run_exists: false }
  const setHidden = (h) => {
    Object.defineProperty(document, 'hidden', { configurable: true, get: () => h })
    document.dispatchEvent(new Event('visibilitychange'))
  }
  const tick = (ms) => act(async () => { await vi.advanceTimersByTimeAsync(ms) })
  const setup = () => { vi.useFakeTimers(); setHidden(false) }
  afterEach(() => { vi.useRealTimers(); setHidden(false) })

  it('a failed probe after a good one leaves the nav item in place', async () => {
    setup()
    const api = { probe: vi.fn().mockResolvedValue(run) }
    const { result } = renderHook(() => useControlRoomProbe(api))
    await tick(0)
    expect(result.current).toEqual({ show: true, running: true })
    api.probe.mockResolvedValue({ error: 'timeout' })
    await tick(15_000)
    expect(api.probe).toHaveBeenCalledTimes(2)
    expect(result.current).toEqual({ show: true, running: true })
    await tick(15_000)
    expect(result.current).toEqual({ show: true, running: true })
  })
  it('a failed first probe is retried after 5 s, then 15 s, then the normal cadence', async () => {
    setup()
    const api = { probe: vi.fn().mockResolvedValue({ error: 'timeout' }) }
    const { result } = renderHook(() => useControlRoomProbe(api))
    await tick(0)
    expect(api.probe).toHaveBeenCalledTimes(1)
    await tick(4_900)
    expect(api.probe).toHaveBeenCalledTimes(1)
    await tick(200)
    expect(api.probe).toHaveBeenCalledTimes(2)
    await tick(14_800)
    expect(api.probe).toHaveBeenCalledTimes(2)
    api.probe.mockResolvedValue(run)
    await tick(300)
    expect(api.probe).toHaveBeenCalledTimes(3)
    expect(result.current).toEqual({ show: true, running: true })
  })
  it('a good probe that says no run does hide it again', async () => {
    setup()
    const api = { probe: vi.fn().mockResolvedValue(run) }
    const { result } = renderHook(() => useControlRoomProbe(api))
    await tick(0)
    api.probe.mockResolvedValue({ mode_available: false, run_exists: false })
    await tick(15_000)
    expect(result.current).toEqual({ show: false, running: false })
  })
  it('asks every 15 s while a run exists and every 60 s when the last answer said none', async () => {
    setup()
    const api = { probe: vi.fn().mockResolvedValue(run) }
    renderHook(() => useControlRoomProbe(api))
    await tick(0)
    await tick(45_000)
    expect(api.probe).toHaveBeenCalledTimes(4)
    api.probe.mockResolvedValue(idle)
    await tick(15_000)               // this probe answers idle
    const n = api.probe.mock.calls.length
    await tick(59_000)
    expect(api.probe).toHaveBeenCalledTimes(n)
    await tick(1_100)
    expect(api.probe).toHaveBeenCalledTimes(n + 1)
  })
  it('a good probe resets the failure count: fail, ok, fail retries after 5 s again, not 15 s', async () => {
    setup()
    const api = { probe: vi.fn().mockResolvedValue({ error: 'x' }) }
    renderHook(() => useControlRoomProbe(api))
    await tick(0)                    // first failure: retry in 5 s
    api.probe.mockResolvedValue(run)
    await tick(5_100)                // ok: next in 15 s (a run exists), not another short retry
    const afterOk = api.probe.mock.calls.length
    await tick(10_000)
    expect(api.probe).toHaveBeenCalledTimes(afterOk)
    api.probe.mockResolvedValue({ error: 'x' })
    await tick(5_100)                // this probe fails
    const n = api.probe.mock.calls.length
    await tick(5_100)
    expect(api.probe).toHaveBeenCalledTimes(n + 1)
  })
  it('does not ask at all while the window is hidden, and asks again on return', async () => {
    setup()
    const api = { probe: vi.fn().mockResolvedValue(run) }
    renderHook(() => useControlRoomProbe(api))
    await tick(0)
    act(() => setHidden(true))
    const n = api.probe.mock.calls.length
    await tick(300_000)
    expect(api.probe).toHaveBeenCalledTimes(n)
    act(() => setHidden(false))
    await tick(0)
    expect(api.probe).toHaveBeenCalledTimes(n + 1)
  })
  it('never has two probes in flight', async () => {
    setup()
    let inFlight = 0, most = 0
    const api = { probe: vi.fn(() => { inFlight++; most = Math.max(most, inFlight); return new Promise(r => setTimeout(() => { inFlight--; r(run) }, 40_000)) }) }
    renderHook(() => useControlRoomProbe(api))
    await tick(200_000)
    expect(most).toBe(1)
  })
})
