// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup } from '@testing-library/react'
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
  it('shows only when the mode is available and a run exists, or the ui flag is on', () => {
    expect(probeShows(null, true)).toBe(false)
    expect(probeShows({ mode_available: false, run_exists: true }, true)).toBe(false)
    expect(probeShows({ mode_available: true, run_exists: false }, false)).toBe(false)
    expect(probeShows({ mode_available: true, run_exists: true }, false)).toBe(true)
    expect(probeShows({ mode_available: true, run_exists: false }, true)).toBe(true)
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
