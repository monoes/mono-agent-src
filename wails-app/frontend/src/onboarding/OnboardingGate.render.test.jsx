// @vitest-environment jsdom
// The gate between the onboarding and the app: GetOnboardingState decides,
// openOnboarding reopens it over a finished state, and a gate that cannot ask
// shows the app.
import React, { useContext, useEffect, useState } from 'react'
import { describe, it, expect, vi, beforeAll, beforeEach, afterEach, afterAll } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, cleanup, act } from '@testing-library/react'

const go = vi.hoisted(() => ({
  IsReady: vi.fn(() => Promise.resolve(true)),
  GetOnboardingState: vi.fn(),
  GetOnboardingStateFull: vi.fn(),
  MarkOnboardingStep: vi.fn(() => Promise.resolve()),
  SkipOnboardingStep: vi.fn(() => Promise.resolve()),
  CompleteOnboarding: vi.fn(() => Promise.resolve()),
  RestartOnboarding: vi.fn(() => Promise.resolve()),
}))
vi.mock('../wailsjs/go/main/App', () => go)
const setup = vi.hoisted(() => ({ stale: 0 }))
vi.mock('../pages/dashboard/setupSession.js', () => ({ markSetupStale: () => { setup.stale += 1 } }))
const appLife = vi.hoisted(() => ({ mounts: 0, keys: [] }))
vi.mock('../App.jsx', async () => {
  const { OnboardingContext } = await import('./onboardingContext.js')
  return {
    default: function FakeApp({ banners, initialNav, navRequest }) {
      const { openOnboarding } = useContext(OnboardingContext)
      const [count, setCount] = useState(0)
      useEffect(() => {
        appLife.mounts += 1
        // an app-wide shortcut, like NodeRunner's Delete
        const onKey = (e) => appLife.keys.push(e.key)
        window.addEventListener('keydown', onKey)
        return () => window.removeEventListener('keydown', onKey)
      }, [])
      return (
        <div data-testid="app">
          {banners}
          <span data-testid="nav">{JSON.stringify(initialNav ?? null)}</span>
          <span data-testid="navRequest">{JSON.stringify(navRequest ?? null)}</span>
          <button type="button" onClick={() => setCount(c => c + 1)}>count {count}</button>
          <button type="button" onClick={() => openOnboarding('monomind')}>reopen</button>
          <button type="button" onClick={() => openOnboarding()}>reopen at current</button>
        </div>
      )
    },
  }
})

import i18n from '../i18n.js'
import OnboardingGate, { LATE_SWITCH_MS } from './OnboardingGate.jsx'
import { STEP_COMPONENTS } from './Onboarding.jsx'
import { requestProfileSetup, pendingProfileSetup, clearProfileSetup } from './profileSetup.js'

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
  go.GetOnboardingStateFull.mockReset()
  // by default the full read answers what the fast one does
  go.GetOnboardingStateFull.mockImplementation(() => { const f = go.GetOnboardingState.getMockImplementation(); return f ? f() : undefined })
  go.IsReady.mockClear()
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

  it('reads the state only once the backend finished startup', async () => {
    go.IsReady.mockResolvedValueOnce(false)
    go.GetOnboardingState.mockResolvedValue(stateOf({ currentStep: 'about' }))
    render(<OnboardingGate banners={null} />)
    await act(async () => { await Promise.resolve() })
    expect(go.GetOnboardingState).not.toHaveBeenCalled()
    expect(screen.getByRole('status')).toHaveTextContent(i18n.t('account.checking'))
    expect(await screen.findByTestId('step-about')).toBeInTheDocument()
    expect(go.IsReady).toHaveBeenCalledTimes(2)
    expect(go.GetOnboardingState).toHaveBeenCalledTimes(1)
  })

  // The app showed at 3 s because the first read missed its deadline; the late
  // answer lands at answerAt ms after mount.
  const lateFirstRead = async (late, answerAt) => {
    vi.useFakeTimers()
    try {
      const log = vi.spyOn(console, 'error').mockImplementation(() => {})
      let answer
      go.GetOnboardingState.mockReturnValue(new Promise(r => { answer = r }))
      go.GetOnboardingStateFull.mockResolvedValue(late)
      render(<OnboardingGate banners={null} />)
      await act(async () => { await vi.advanceTimersByTimeAsync(2900) })
      expect(go.GetOnboardingState).toHaveBeenCalledTimes(1)
      expect(screen.getByRole('status')).toHaveTextContent(i18n.t('account.checking'))
      await act(async () => { await vi.advanceTimersByTimeAsync(200) })
      expect(screen.getByTestId('app')).toBeInTheDocument()
      expect(log).toHaveBeenCalled()
      await act(async () => { await vi.advanceTimersByTimeAsync(answerAt - 3100) })
      await act(async () => { answer(late) })
      await act(async () => { await vi.advanceTimersByTimeAsync(10) })
    } finally {
      vi.useRealTimers()
    }
  }

  it('shows the app after 3 s; a late "not completed" within 10 s of that still opens the onboarding', async () => {
    await lateFirstRead(stateOf({ currentStep: 'system' }), 12000)
    expect(screen.queryByTestId('app')).not.toBeInTheDocument()
    expect(heading()).toHaveTextContent('System check')
  })

  it('leaves the app when the late "not completed" lands more than 10 s after the app showed', async () => {
    await lateFirstRead(stateOf({ currentStep: 'system' }), 3100 + LATE_SWITCH_MS + 500)
    expect(screen.getByTestId('app')).toBeInTheDocument()
    expect(screen.queryByRole('heading', { level: 1 })).not.toBeInTheDocument()
  })

  it('leaves the app when the late answer says completed', async () => {
    await lateFirstRead(stateOf({ completed: true, currentStep: 'finish' }), 4000)
    expect(screen.getByTestId('app')).toBeInTheDocument()
  })

  it('paints the fast answer, then shows the statuses of the full read', async () => {
    go.GetOnboardingState.mockResolvedValue(stateOf({ currentStep: 'about' }))
    const fullSteps = IDS.map(id => ({ id, status: ['welcome', 'system', 'profile', 'monomind', 'browser'].includes(id) ? 'done' : 'todo' }))
    go.GetOnboardingStateFull.mockResolvedValue(stateOf({ currentStep: 'about', steps: fullSteps }))
    render(<OnboardingGate banners={null} />)
    expect(await screen.findByTestId('step-about')).toBeInTheDocument()
    await act(async () => { await Promise.resolve() })
    expect(go.GetOnboardingStateFull).toHaveBeenCalledTimes(1)
    expect(document.querySelector('[data-step="monomind"]')).toHaveAttribute('data-status', 'done')
    expect(document.querySelector('[data-step="browser"]')).toHaveAttribute('data-status', 'done')
    expect(document.querySelector('[data-step="agents"]')).toHaveAttribute('data-status', 'todo')
  })

  it('a readable state without completed: false shows the app', async () => {
    const { completed, ...noFlag } = stateOf()
    go.GetOnboardingState.mockResolvedValue(noFlag)
    render(<OnboardingGate banners={null} />)
    expect(await screen.findByTestId('app')).toBeInTheDocument()
    expect(completed).toBe(false)
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
    go.GetOnboardingStateFull.mockResolvedValue(stateOf({ completed: true, currentStep: 'finish' }))
    render(<OnboardingGate banners={null} />)
    fireEvent.click(await screen.findByText('reopen'))
    expect(await screen.findByTestId('step-monomind')).toBeInTheDocument()
    expect(heading()).toHaveTextContent('Turn on monomind')
    // it asks again, with every fact, so a restart from inside the app shows the new statuses
    expect(go.GetOnboardingState).toHaveBeenCalledTimes(1)
    expect(go.GetOnboardingStateFull).toHaveBeenCalledTimes(1)
    // and keeps reading the full state while it is open
    await act(async () => { await seen.monomind.mark('monomind') })
    expect(go.GetOnboardingStateFull).toHaveBeenCalledTimes(2)
    expect(go.GetOnboardingState).toHaveBeenCalledTimes(1)
  })

  it('openOnboarding() opens at the current step; finishing brings the app back with its nav', async () => {
    go.GetOnboardingState.mockResolvedValue(stateOf({ completed: true, currentStep: 'finish' }))
    go.GetOnboardingStateFull.mockResolvedValue(stateOf({ completed: true, currentStep: 'agents' }))
    render(<OnboardingGate banners={null} />)
    fireEvent.click(await screen.findByText('reopen at current'))
    expect(await screen.findByTestId('step-agents')).toBeInTheDocument()
    act(() => seen.agents.onFinish({ page: 'orgs', opts: { tab: 'new' } }))
    expect(screen.queryByTestId('step-agents')).not.toBeInTheDocument()
    // the app was underneath all along: it is navigated, not remounted
    expect(screen.getByTestId('navRequest')).toHaveTextContent('{"page":"orgs","opts":{"tab":"new"}}')
    expect(screen.getByTestId('nav')).toHaveTextContent('null')
  })

  it('a step\'s mark() writes the mark and reads the state again', async () => {
    go.GetOnboardingState.mockResolvedValue(stateOf())
    render(<OnboardingGate banners={null} />)
    await screen.findByTestId('step-welcome')
    // a first answer of "not completed" is followed by one full read at once
    await act(async () => { await Promise.resolve() })
    expect(go.GetOnboardingStateFull).toHaveBeenCalledTimes(1)
    go.GetOnboardingStateFull.mockResolvedValue(stateOf({ currentStep: 'system', steps: IDS.map(id => ({ id, status: id === 'welcome' ? 'done' : 'todo' })) }))
    let r
    await act(async () => { r = await seen.welcome.mark('welcome') })
    expect(r).toEqual({ ok: true, value: undefined })
    expect(go.MarkOnboardingStep).toHaveBeenCalledWith('welcome')
    // the re-read after a mark is the full one
    expect(go.GetOnboardingState).toHaveBeenCalledTimes(1)
    expect(go.GetOnboardingStateFull).toHaveBeenCalledTimes(2)
    expect(document.querySelector('[data-step="welcome"]')).toHaveAttribute('data-status', 'done')
    // still on the step the person is on: a reload never moves them
    expect(heading()).toHaveTextContent('Welcome')
  })

  it('finishing a first run after complete() shows the app, not the onboarding again', async () => {
    go.GetOnboardingState.mockResolvedValue(stateOf({ currentStep: 'finish' }))
    render(<OnboardingGate banners={null} />)
    await screen.findByTestId('step-finish')
    go.GetOnboardingStateFull.mockResolvedValue(stateOf({ completed: true, currentStep: 'finish' }))
    await act(async () => { await seen.finish.complete() })
    expect(go.CompleteOnboarding).toHaveBeenCalled()
    act(() => seen.finish.onFinish())
    expect(await screen.findByTestId('app')).toBeInTheDocument()
  })
})

// After NewProfileModal created a later profile, the caller switched to it and
// reloaded the window; the gate opens the short setup for that profile.
describe('the short setup for a later profile, after the reload', () => {
  beforeEach(() => { clearProfileSetup() })
  afterEach(() => { clearProfileSetup() })
  const later = (over = {}) => stateOf({ completed: true, currentStep: 'finish', profileId: 'p2',
    steps: IDS.map(id => ({ id, status: ['about', 'monomind', 'browser', 'agents'].includes(id) ? 'todo' : 'done' })), ...over })

  it('opens profile mode for the selected new profile, reading the full state', async () => {
    requestProfileSetup('p2')
    go.GetOnboardingState.mockResolvedValue(later())
    go.GetOnboardingStateFull.mockResolvedValue(later({ steps: IDS.map(id => ({ id, status: ['about', 'browser', 'agents'].includes(id) ? 'todo' : 'done' })) }))
    render(<OnboardingGate banners={null} />)
    expect(await screen.findByTestId('step-about')).toBeInTheDocument()
    // an existing install: the app is mounted underneath, inert
    expect(document.querySelector('.onb-app-host')).toHaveAttribute('inert')
    expect(screen.getByRole('button', { name: 'Back to MonoAgent' })).toBeInTheDocument()
    expect(seen.about.mode).toBe('profile')
    expect(document.querySelectorAll('nav[aria-label="Setup progress"] li')).toHaveLength(3)
    await act(async () => { await Promise.resolve() })
    expect(go.GetOnboardingStateFull).toHaveBeenCalledTimes(1)
    expect(document.querySelector('[data-step="monomind"]')).toHaveAttribute('data-status', 'done')
    expect(pendingProfileSetup()).toBeNull()
    // Done closes it into the app, without completing anything
    act(() => seen.about.onFinish())
    expect(screen.queryByTestId('step-about')).not.toBeInTheDocument()
    expect(document.querySelector('.onb-app-host')).not.toHaveAttribute('inert')
    expect(go.CompleteOnboarding).not.toHaveBeenCalled()
  })

  it('shows the app and drops the request when the new profile is not the selected one', async () => {
    requestProfileSetup('p2')
    go.GetOnboardingState.mockResolvedValue(later({ profileId: 'p1' }))
    render(<OnboardingGate banners={null} />)
    expect(await screen.findByTestId('app')).toBeInTheDocument()
    await act(async () => { await Promise.resolve() })
    expect(pendingProfileSetup()).toBeNull()
  })

  it('an unfinished install shows the full flow and drops the request', async () => {
    requestProfileSetup('p2')
    go.GetOnboardingState.mockResolvedValue(later({ completed: false, currentStep: 'about' }))
    render(<OnboardingGate banners={null} />)
    expect(await screen.findByTestId('step-about')).toBeInTheDocument()
    expect(seen.about.mode).toBe('full')
    await act(async () => { await Promise.resolve() })
    expect(pendingProfileSetup()).toBeNull()
  })
})

// Reopened from inside the app (the Finish setup card, "Run setup again", the
// short setup): the app stays mounted underneath and keeps its state.
describe('the onboarding over the running app', () => {
  beforeEach(() => { appLife.mounts = 0; appLife.keys = []; setup.stale = 0 })
  const completed = () => go.GetOnboardingState.mockResolvedValue(stateOf({ completed: true, currentStep: 'agents' }))
  const appHost = () => document.querySelector('.onb-app-host')
  const reopen = async () => {
    fireEvent.click(await screen.findByText('reopen at current'))
    expect(await screen.findByTestId('step-agents')).toBeInTheDocument()
  }

  it('keeps the app mounted underneath with its state, inert and hidden from assistive tech', async () => {
    completed()
    render(<OnboardingGate banners={null} />)
    fireEvent.click(await screen.findByText('count 0'))
    fireEvent.click(screen.getByText('count 1'))
    await reopen()
    expect(appHost()).toHaveAttribute('inert')
    expect(appHost()).toHaveAttribute('aria-hidden', 'true')
    expect(screen.queryByRole('button', { name: 'count 2' })).not.toBeInTheDocument() // hidden from assistive tech
    expect(screen.getByText('count 2')).toBeInTheDocument() //                           but still there, with its state
    fireEvent.click(screen.getByRole('button', { name: 'Back to MonoAgent' }))
    expect(screen.queryByTestId('step-agents')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'count 2' })).toBeInTheDocument()
    expect(appHost()).not.toHaveAttribute('inert')
    expect(appLife.mounts).toBe(1)
  })

  it('every close marks the Finish setup card\'s answer stale: Back, Escape, finishing', async () => {
    completed()
    render(<OnboardingGate banners={null} />)
    await screen.findByText('reopen at current')
    expect(setup.stale).toBe(0)
    await reopen()
    expect(setup.stale).toBe(0)
    fireEvent.click(screen.getByRole('button', { name: 'Back to MonoAgent' }))
    expect(setup.stale).toBe(1)
    await reopen()
    fireEvent.keyDown(screen.getByRole('heading', { level: 1 }), { key: 'Escape' })
    expect(setup.stale).toBe(2)
    await reopen()
    act(() => seen.agents.onFinish({ page: 'orgs' }))
    expect(setup.stale).toBe(3)
  })

  it('a first run that finishes marks it stale too', async () => {
    go.GetOnboardingState.mockResolvedValue(stateOf({ currentStep: 'finish' }))
    render(<OnboardingGate banners={null} />)
    await screen.findByTestId('step-finish')
    act(() => seen.finish.onFinish())
    expect(await screen.findByTestId('app')).toBeInTheDocument()
    expect(setup.stale).toBe(1)
  })

  it('Back to MonoAgent closes without complete() and without writing anything', async () => {
    completed()
    render(<OnboardingGate banners={null} />)
    await reopen()
    const reads = go.GetOnboardingStateFull.mock.calls.length
    fireEvent.click(screen.getByRole('button', { name: 'Back to MonoAgent' }))
    await act(async () => { await Promise.resolve() })
    expect(go.CompleteOnboarding).not.toHaveBeenCalled()
    expect(go.MarkOnboardingStep).not.toHaveBeenCalled()
    expect(go.SkipOnboardingStep).not.toHaveBeenCalled()
    expect(go.RestartOnboarding).not.toHaveBeenCalled()
    expect(go.GetOnboardingStateFull.mock.calls.length).toBe(reads)
  })

  it('the control is absent on a first run, and the app is not mounted', async () => {
    go.GetOnboardingState.mockResolvedValue(stateOf({ currentStep: 'system' }))
    render(<OnboardingGate banners={null} />)
    expect(await screen.findByTestId('step-system')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Back to MonoAgent' })).not.toBeInTheDocument()
    expect(appHost()).toBeNull()
    expect(appLife.mounts).toBe(0)
  })

  it('finishing over the running app calls its navigate, not a remount', async () => {
    completed()
    render(<OnboardingGate banners={null} />)
    await reopen()
    act(() => seen.agents.onFinish({ page: 'tasks' }))
    expect(screen.getByTestId('navRequest')).toHaveTextContent('{"page":"tasks"}')
    expect(appLife.mounts).toBe(1)
  })

  it('Escape closes only over the running app, and not while a step is busy', async () => {
    completed()
    render(<OnboardingGate banners={null} />)
    await reopen()
    act(() => { seen.agents.setCanContinue(true); seen.agents.setOnContinue(() => new Promise(() => {})) })
    fireEvent.click(screen.getByRole('button', { name: 'Continue' }))
    fireEvent.keyDown(screen.getByRole('heading', { level: 1 }), { key: 'Escape' })
    expect(screen.getByTestId('step-agents')).toBeInTheDocument()
    cleanup()

    completed()
    render(<OnboardingGate banners={null} />)
    await reopen()
    fireEvent.keyDown(screen.getByRole('heading', { level: 1 }), { key: 'Escape' })
    expect(screen.queryByTestId('step-agents')).not.toBeInTheDocument()
    cleanup()

    go.GetOnboardingState.mockResolvedValue(stateOf({ currentStep: 'system' }))
    render(<OnboardingGate banners={null} />)
    expect(await screen.findByTestId('step-system')).toBeInTheDocument()
    fireEvent.keyDown(screen.getByRole('heading', { level: 1 }), { key: 'Escape' })
    expect(screen.getByTestId('step-system')).toBeInTheDocument()
  })

  it("keys pressed in the onboarding never reach the app's own listeners", async () => {
    completed()
    render(<OnboardingGate banners={null} />)
    await reopen()
    fireEvent.keyDown(screen.getByRole('heading', { level: 1 }), { key: 'Backspace' })
    fireEvent.keyDown(screen.getByRole('heading', { level: 1 }), { key: 'n' })
    // nor a key aimed at the page body while it is open
    fireEvent.keyDown(document.body, { key: 'Delete' })
    expect(appLife.keys).toEqual([])
    fireEvent.click(screen.getByRole('button', { name: 'Back to MonoAgent' }))
    fireEvent.keyDown(document.body, { key: 'n' })
    expect(appLife.keys).toEqual(['n'])
  })

  it('open and close cycles leave no listener of the scene or the shell behind', async () => {
    completed()
    const added = new Map(), removed = new Map()
    const count = (m, type) => m.set(type, (m.get(type) || 0) + 1)
    const wa = window.addEventListener, wr = window.removeEventListener
    const da = document.addEventListener, dr = document.removeEventListener
    window.addEventListener = function (type, ...r) { count(added, 'w:' + type); return wa.call(this, type, ...r) }
    window.removeEventListener = function (type, ...r) { count(removed, 'w:' + type); return wr.call(this, type, ...r) }
    document.addEventListener = function (type, ...r) { count(added, 'd:' + type); return da.call(this, type, ...r) }
    document.removeEventListener = function (type, ...r) { count(removed, 'd:' + type); return dr.call(this, type, ...r) }
    try {
      render(<OnboardingGate banners={null} />)
      await screen.findByText('reopen at current')
      added.clear(); removed.clear()
      for (let i = 0; i < 3; i++) {
        await reopen()
        fireEvent.click(screen.getByRole('button', { name: 'Back to MonoAgent' }))
      }
      expect(added.get('w:resize')).toBe(3)
      for (const [type, n] of added) expect([type, removed.get(type) || 0]).toEqual([type, n])
      expect(document.querySelectorAll('.onb-portal')).toHaveLength(0)
    } finally {
      window.addEventListener = wa; window.removeEventListener = wr
      document.addEventListener = da; document.removeEventListener = dr
    }
  })
})
