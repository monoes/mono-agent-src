// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
import NewProfileModal from './NewProfileModal.jsx'
import { pendingProfileSetup, clearProfileSetup } from '../onboarding/profileSetup.js'
import OnboardingGate from '../onboarding/OnboardingGate.jsx'
import { STEP_COMPONENTS } from '../onboarding/Onboarding.jsx'

const mockCreateProfile = vi.fn()
const mockChooseProfileFolder = vi.fn()
const mockListMonomindProjects = vi.fn()

// After the reload the onboarding gate reads the state: a completed install
// whose selected profile is the new one, p2.
const IDS = ['welcome', 'system', 'profile', 'about', 'monomind', 'agents', 'browser', 'permissions', 'firstRun', 'finish']
const laterProfileState = () => ({ completed: true, currentStep: 'finish', profileId: 'p2',
  steps: IDS.map(id => ({ id, status: ['about', 'monomind', 'browser', 'agents'].includes(id) ? 'todo' : 'done' })) })
vi.mock('../wailsjs/go/main/App', () => ({
  CreateProfile: (...args) => mockCreateProfile(...args),
  ChooseProfileFolder: (...args) => mockChooseProfileFolder(...args),
  ListMonomindProjects: (...args) => mockListMonomindProjects(...args),
  IsReady: () => Promise.resolve(true),
  GetOnboardingState: () => Promise.resolve(laterProfileState()),
  GetOnboardingStateFull: () => Promise.resolve(laterProfileState()),
}))
vi.mock('../App.jsx', () => ({ default: () => <div data-testid="app" /> }))

// Matches Sidebar.render.test.jsx's convention: assert against the raw
// translation key rather than bootstrapping real i18next in a unit test.
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key) => key }),
}))

beforeEach(() => {
  vi.clearAllMocks()
  // jsdom doesn't implement scrollIntoView; IconPickerModal calls it in an
  // effect whenever its focused tile changes, which crashes the whole tree
  // in a jsdom test environment otherwise (real browsers have it).
  Element.prototype.scrollIntoView = vi.fn()
  mockListMonomindProjects.mockResolvedValue([])
  mockCreateProfile.mockResolvedValue({ id: 'new-id', name: 'x' })
  global.fetch = vi.fn(() =>
    Promise.resolve({
      json: () => Promise.resolve({
        agents: [
          { id: 'coder', label: 'Coder', category: 'Tech' },
          { id: 'writer', label: 'Writer', category: 'Content' },
        ],
      }),
    }),
  )
})

afterEach(() => {
  cleanup()
  delete global.fetch
})

it('renders nothing when closed', () => {
  const { container } = render(<NewProfileModal open={false} onClose={() => {}} onCreated={() => {}} />)
  expect(container).toBeEmptyDOMElement()
})

it('renders a centered dialog with a name field when open', async () => {
  render(<NewProfileModal open={true} onClose={() => {}} onCreated={() => {}} />)
  expect(screen.getByRole('dialog', { name: 'newProfileModal.title' })).toBeInTheDocument()
  expect(screen.getByPlaceholderText('newProfileModal.namePlaceholder')).toBeInTheDocument()
  await waitFor(() => expect(mockListMonomindProjects).toHaveBeenCalled())
})

it('creates a profile with the typed name and no folder/icon by default', async () => {
  const onCreated = vi.fn()
  const onClose = vi.fn()
  render(<NewProfileModal open={true} onClose={onClose} onCreated={onCreated} />)

  fireEvent.change(screen.getByPlaceholderText('newProfileModal.namePlaceholder'), { target: { value: 'Election Campaign' } })
  fireEvent.click(screen.getByText('newProfileModal.create'))

  await waitFor(() => expect(mockCreateProfile).toHaveBeenCalledWith('Election Campaign', '', ''))
  await waitFor(() => expect(onCreated).toHaveBeenCalled())
  expect(onClose).toHaveBeenCalled()
})

it('lists suggested monomind projects and picking one fills name and folder', async () => {
  mockListMonomindProjects.mockResolvedValue([
    { path: '/Users/morteza/Desktop/monoes/monomind', name: 'monomind' },
  ])
  render(<NewProfileModal open={true} onClose={() => {}} onCreated={() => {}} />)

  const projectRow = await screen.findByText('monomind')
  fireEvent.click(projectRow)

  expect(screen.getByPlaceholderText('newProfileModal.namePlaceholder')).toHaveValue('monomind')
  expect(screen.getByText('→ /Users/morteza/Desktop/monoes/monomind')).toBeInTheDocument()

  fireEvent.click(screen.getByText('newProfileModal.create'))
  await waitFor(() =>
    expect(mockCreateProfile).toHaveBeenCalledWith('monomind', '/Users/morteza/Desktop/monoes/monomind', ''),
  )
})

it('does not render any project suggestions when there are none', async () => {
  render(<NewProfileModal open={true} onClose={() => {}} onCreated={() => {}} />)
  await waitFor(() => expect(mockListMonomindProjects).toHaveBeenCalled())
  expect(screen.queryByText('newProfileModal.suggestedProjects')).not.toBeInTheDocument()
})

it('picking an icon updates the preview and is included in the create call', async () => {
  render(<NewProfileModal open={true} onClose={() => {}} onCreated={() => {}} />)

  fireEvent.click(screen.getByTitle('newProfileModal.chooseIcon'))
  const writerTile = await screen.findByTitle('Writer')
  fireEvent.click(writerTile)

  fireEvent.change(screen.getByPlaceholderText('newProfileModal.namePlaceholder'), { target: { value: 'Content Team' } })
  fireEvent.click(screen.getByText('newProfileModal.create'))

  await waitFor(() => expect(mockCreateProfile).toHaveBeenCalledWith('Content Team', '', 'writer'))
})

it('backdrop click and Escape both close the modal', () => {
  const onClose = vi.fn()
  const { container } = render(<NewProfileModal open={true} onClose={onClose} onCreated={() => {}} />)

  fireEvent.click(container.querySelector('.modal-overlay'))
  expect(onClose).toHaveBeenCalledTimes(1)

  fireEvent.keyDown(window, { key: 'Escape' })
  expect(onClose).toHaveBeenCalledTimes(2)
})

// The short setup for a later profile (onboarding mode 'profile'). The modal
// only records the request; the caller switches to the new profile and reloads
// the window, and the onboarding gate opens the short flow for it then.
describe('the short setup for a later profile', () => {
  beforeEach(() => { clearProfileSetup() })
  afterEach(() => { clearProfileSetup() })

  it('creating a second profile opens the short flow', async () => {
    // Go selects only the first profile at once: a later one comes back not active
    mockCreateProfile.mockResolvedValue({ id: 'p2', name: 'Work', is_active: false })
    const order = []
    const { unmount } = render(<NewProfileModal open={true} onClose={() => order.push('close')} onCreated={() => order.push(`created:${pendingProfileSetup()}`)} />)
    fireEvent.change(screen.getByPlaceholderText('newProfileModal.namePlaceholder'), { target: { value: 'Work' } })
    fireEvent.click(screen.getByText('newProfileModal.create'))
    await waitFor(() => expect(order).toEqual(['created:p2', 'close']))
    // The caller (Sidebar) switches to p2 and reloads the window; after the
    // reload the gate opens the short setup for p2, over the app.
    unmount()
    const original = { ...STEP_COMPONENTS }
    for (const id of IDS) STEP_COMPONENTS[id] = () => <div data-testid={`step-${id}`} />
    try {
      render(<OnboardingGate banners={null} />)
      expect(await screen.findByTestId('step-about')).toBeInTheDocument()
      expect(document.querySelector('.onb-root')).toHaveAttribute('data-mode', 'profile')
      expect([...document.querySelectorAll('.onb-journey li')].map(li => li.getAttribute('data-step'))).toEqual(['about', 'monomind', 'browser'])
      expect(pendingProfileSetup()).toBeNull()
    } finally {
      Object.assign(STEP_COMPONENTS, original)
    }
  })

  it('creating the very first profile does not open the short flow', async () => {
    mockCreateProfile.mockResolvedValue({ id: 'p1', name: 'Personal', is_active: true })
    const onCreated = vi.fn()
    render(<NewProfileModal open={true} onClose={() => {}} onCreated={onCreated} />)
    fireEvent.change(screen.getByPlaceholderText('newProfileModal.namePlaceholder'), { target: { value: 'Personal' } })
    fireEvent.click(screen.getByText('newProfileModal.create'))
    await waitFor(() => expect(onCreated).toHaveBeenCalled())
    expect(pendingProfileSetup()).toBeNull()
  })

  it('a failed create asks for nothing', async () => {
    mockCreateProfile.mockRejectedValue(new Error('name taken'))
    render(<NewProfileModal open={true} onClose={() => {}} onCreated={() => {}} />)
    fireEvent.change(screen.getByPlaceholderText('newProfileModal.namePlaceholder'), { target: { value: 'Work' } })
    fireEvent.click(screen.getByText('newProfileModal.create'))
    expect(await screen.findByText('name taken')).toBeInTheDocument()
    expect(pendingProfileSetup()).toBeNull()
  })
})
