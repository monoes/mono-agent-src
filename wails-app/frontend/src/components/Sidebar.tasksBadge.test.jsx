// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { act, cleanup, render, screen, waitFor } from '@testing-library/react'

const { api, bus, app } = vi.hoisted(() => ({
  api: { pulse: vi.fn() },
  bus: { fire: () => {} },
  app: { ready: true },
}))

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key) => key }) }))
vi.mock('../services/tasks.js', () => ({ tasksApi: api, onTasksChanged: (f) => { bus.fire = f; return () => {} } }))
vi.mock('../wailsjs/go/main/App', () => ({
  GetVersion: () => Promise.resolve({ version: '1.0.0' }),
  GetHILItems: () => Promise.resolve([]),
  GetProfiles: () => Promise.resolve([{ id: 'default', name: 'Default', is_active: true, root_dir: '', icon: '' }]),
  SwitchProfile: () => Promise.resolve(),
  ChooseProfileFolder: () => Promise.resolve(''),
  MoveProfileFolder: () => Promise.resolve(),
  RevealProfileFolder: () => Promise.resolve(),
  IsReady: () => Promise.resolve(app.ready),
}))
vi.mock('./NewProfileModal.jsx', () => ({ default: () => null }))
import Sidebar from './Sidebar.jsx'

const mount = () => render(<Sidebar activePage="dashboard" onNavigate={() => {}} stats={{}} dbConnected />)
const badge = () => screen.getByText('sidebar.nav.tasks').closest('button, a, div')

beforeEach(() => { vi.clearAllMocks(); app.ready = true })
afterEach(cleanup)

describe('Tasks sidebar badge', () => {
  it('shows Inbox plus Review from the first read', async () => {
    api.pulse.mockResolvedValue({ profile_id: 'default', rev: 3, inbox: 2, review: 1 })
    mount()
    await waitFor(() => expect(badge()).toHaveTextContent('3'))
  })

  it('adds the session plans awaiting approval to the badge', async () => {
    api.pulse.mockResolvedValue({ profile_id: 'default', rev: 3, inbox: 2, review: 1, drafts: 2 })
    mount()
    await waitFor(() => expect(badge()).toHaveTextContent('5'))
  })

  it('never lets an older revision of the same profile replace a newer one', async () => {
    let answer
    api.pulse.mockReturnValue(new Promise(r => { answer = r }))
    mount()
    await waitFor(() => expect(api.pulse).toHaveBeenCalled())
    act(() => bus.fire({ profile_id: 'default', rev: 9, inbox: 5, review: 0 }))
    await waitFor(() => expect(badge()).toHaveTextContent('5'))
    await act(async () => { answer({ profile_id: 'default', rev: 4, inbox: 1, review: 0 }) })
    expect(badge()).toHaveTextContent('5')
  })

  it('lets another profile replace the value', async () => {
    api.pulse.mockResolvedValue({ profile_id: 'default', rev: 9, inbox: 5, review: 0 })
    mount()
    await waitFor(() => expect(badge()).toHaveTextContent('5'))
    act(() => bus.fire({ profile_id: 'work', rev: 1, inbox: 2, review: 0 }))
    await waitFor(() => expect(badge()).toHaveTextContent('2'))
  })

  it('does not ask before the app is ready', async () => {
    app.ready = false
    api.pulse.mockResolvedValue({ profile_id: 'default', rev: 1, inbox: 1, review: 0 })
    mount()
    await new Promise(r => setTimeout(r, 50))
    expect(api.pulse).not.toHaveBeenCalled()
  })
})
