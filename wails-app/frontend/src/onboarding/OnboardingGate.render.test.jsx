// @vitest-environment jsdom
// The gate between the onboarding and the app: GetOnboardingState decides,
// openOnboarding reopens it over a finished state, and a gate that cannot ask
// shows the app.
import React, { useContext } from 'react'
import { describe, it, expect, vi, beforeAll, beforeEach, afterEach, afterAll } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, cleanup, act } from '@testing-library/react'

const go = vi.hoisted(() => ({
  GetOnboardingState: vi.fn(),
  MarkOnboardingStep: vi.fn(() => Promise.resolve()),
  SkipOnboardingStep: vi.fn(() => Promise.resolve()),
  CompleteOnboarding: vi.fn(() => Promise.resolve()),
  RestartOnboarding: vi.fn(() => Promise.resolve()),
}))
vi.mock('../wailsjs/go/main/App', () => go)
vi.mock('../App.jsx', async () => {
  const { OnboardingContext } = await import('./onboardingContext.js')
  return {
    default: function FakeApp({ banners, initialNav }) {
      const { openOnboarding } = useContext(OnboardingContext)
      return (
        <div data-testid="app">
          {banners}
          <span data-testid="nav">{JSON.stringify(initialNav ?? null)}</span>
          <button type="button" onClick={() => openOnboarding('monomind')}>reopen</button>
          <button type="button" onClick={() => openOnboarding()}>reopen at current</button>
        </div>
      )
    },
  }
})

import i18n from '../i18n.js'
import OnboardingGate from './OnboardingGate.jsx'
import { STEP_COMPONENTS } from './Onboarding.jsx'

const IDS = ['welcome', 'system', 'profile', 'about', 'monomind', 'agents', 'browser', 'permissions', 'firstRun', 'finish']
const original = { ...STEP_COMPONENTS }
const seen = {}
beforeAll(() => {
  for (const id of IDS) STEP_COMPONENTS[id] = function StubStep(props) { seen[id] = props; return <div data-testid={`step-${id}`} /> }
})
afterAll(() => { Object.assign(STEP_COMPONENTS, original) })

const stateOf = (over = {}) => ({ completed: false, currentStep: 'welcome', profileId: '', steps: IDS.map(id => ({ id, status: 'todo' })), ...over })
beforeEach(async () => {
  await i18n.changeLanguage('en')
  go.GetOnboardingState.mockReset()
  go.MarkOnboardingStep.mockClear()
  go.CompleteOnboarding.mockClear()
})
afterEach(() => { cleanup(); vi.restoreAllMocks() })

const heading = () => screen.getByRole('heading', { level: 1 })

describe('the onboarding gate', () => {
  it('gate renders App when completed', async () => {
    go.GetOnboardingState.mockResolvedValue(stateOf({ completed: true, currentStep: 'finish' }))
    render(<OnboardingGate banners={<p>banner</p>} />)
    expect(await screen.findByTestId('app')).toBeInTheDocument()
    expect(screen.getByText('banner')).toBeInTheDocument()
    expect(screen.getByTestId('nav')).toHaveTextContent('null')
    expect(screen.queryByRole('heading', { level: 1 })).not.toBeInTheDocument()
  })

  it('gate renders Onboarding when not completed', async () => {
    go.GetOnboardingState.mockResolvedValue(stateOf({ currentStep: 'system' }))
    render(<OnboardingGate banners={null} />)
    expect(await screen.findByTestId('step-system')).toBeInTheDocument()
    expect(heading()).toHaveTextContent('System check')
    expect(screen.queryByTestId('app')).not.toBeInTheDocument()
  })

  it('shows the quiet checking view while the state loads', async () => {
    let answer
    go.GetOnboardingState.mockReturnValue(new Promise(r => { answer = r }))
    render(<OnboardingGate banners={null} />)
    expect(screen.getByRole('status')).toHaveTextContent(i18n.t('account.checking'))
    expect(screen.queryByTestId('app')).not.toBeInTheDocument()
    expect(screen.queryByRole('heading', { level: 1 })).not.toBeInTheDocument()
    await act(async () => { answer(stateOf({ completed: true })) })
    expect(screen.getByTestId('app')).toBeInTheDocument()
  })

  it('renders the app and logs when the state cannot be read', async () => {
    const log = vi.spyOn(console, 'error').mockImplementation(() => {})
    go.GetOnboardingState.mockRejectedValue('binding missing')
    render(<OnboardingGate banners={null} />)
    expect(await screen.findByTestId('app')).toBeInTheDocument()
    expect(log).toHaveBeenCalled()
  })

  it('openOnboarding(stepId) shows Onboarding at that step over a completed state', async () => {
    go.GetOnboardingState.mockResolvedValue(stateOf({ completed: true, currentStep: 'finish' }))
    render(<OnboardingGate banners={null} />)
    fireEvent.click(await screen.findByText('reopen'))
    expect(await screen.findByTestId('step-monomind')).toBeInTheDocument()
    expect(heading()).toHaveTextContent('Turn on monomind')
    // it asks again, so a restart from inside the app shows the new statuses
    expect(go.GetOnboardingState).toHaveBeenCalledTimes(2)
  })

  it('openOnboarding() opens at the current step; finishing brings the app back with its nav', async () => {
    go.GetOnboardingState.mockResolvedValue(stateOf({ completed: true, currentStep: 'agents' }))
    render(<OnboardingGate banners={null} />)
    fireEvent.click(await screen.findByText('reopen at current'))
    expect(await screen.findByTestId('step-agents')).toBeInTheDocument()
    act(() => seen.agents.onFinish({ page: 'orgs', opts: { tab: 'new' } }))
    expect(await screen.findByTestId('app')).toBeInTheDocument()
    expect(screen.getByTestId('nav')).toHaveTextContent('{"page":"orgs","opts":{"tab":"new"}}')
  })

  it('a step\'s mark() writes the mark and reads the state again', async () => {
    go.GetOnboardingState.mockResolvedValue(stateOf())
    render(<OnboardingGate banners={null} />)
    await screen.findByTestId('step-welcome')
    go.GetOnboardingState.mockResolvedValue(stateOf({ currentStep: 'system', steps: IDS.map(id => ({ id, status: id === 'welcome' ? 'done' : 'todo' })) }))
    let r
    await act(async () => { r = await seen.welcome.mark('welcome') })
    expect(r).toEqual({ ok: true, value: undefined })
    expect(go.MarkOnboardingStep).toHaveBeenCalledWith('welcome')
    expect(go.GetOnboardingState).toHaveBeenCalledTimes(2)
    expect(document.querySelector('[data-step="welcome"]')).toHaveAttribute('data-status', 'done')
    // still on the step the person is on: a reload never moves them
    expect(heading()).toHaveTextContent('Welcome')
  })

  it('finishing a first run after complete() shows the app, not the onboarding again', async () => {
    go.GetOnboardingState.mockResolvedValue(stateOf({ currentStep: 'finish' }))
    render(<OnboardingGate banners={null} />)
    await screen.findByTestId('step-finish')
    go.GetOnboardingState.mockResolvedValue(stateOf({ completed: true, currentStep: 'finish' }))
    await act(async () => { await seen.finish.complete() })
    expect(go.CompleteOnboarding).toHaveBeenCalled()
    act(() => seen.finish.onFinish())
    expect(await screen.findByTestId('app')).toBeInTheDocument()
  })
})
