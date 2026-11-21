// @vitest-environment jsdom
// The onboarding shell: which step it opens at, the footer (Back, Skip for now,
// Continue), Enter, the rail. The steps are swapped for stubs, so these tests
// keep testing the shell when the real steps replace the placeholders.
import React from 'react'
import { describe, it, expect, vi, beforeAll, beforeEach, afterEach, afterAll } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, cleanup, act, within } from '@testing-library/react'
import i18n from '../i18n.js'
import Onboarding, { STEP_COMPONENTS } from './Onboarding.jsx'
import { STEPS } from './steps.js'

const IDS = ['welcome', 'system', 'profile', 'about', 'monomind', 'agents', 'browser', 'permissions', 'firstRun', 'finish']
const original = { ...STEP_COMPONENTS }
const seen = {}
beforeAll(() => {
  for (const id of IDS) {
    STEP_COMPONENTS[id] = function StubStep(props) {
      seen[id] = props
      return (
        <div data-testid={`step-${id}`}>
          <button type="button" onClick={() => props.setCanContinue(true)}>allow</button>
          <textarea aria-label="notes" />
          <input aria-label="name" type="text" />
        </div>
      )
    }
  }
})
afterAll(() => { Object.assign(STEP_COMPONENTS, original) })

const stateOf = (over = {}, statuses = {}) => ({
  completed: false, currentStep: 'welcome', profileId: 'p1',
  steps: IDS.map(id => ({ id, status: statuses[id] || 'todo' })), ...over,
})
let actions
beforeEach(async () => {
  await i18n.changeLanguage('en')
  actions = {
    reload: vi.fn(() => Promise.resolve({ ok: true })),
    mark: vi.fn(() => Promise.resolve({ ok: true })),
    skip: vi.fn(() => Promise.resolve({ ok: true })),
    complete: vi.fn(() => Promise.resolve({ ok: true })),
    onFinish: vi.fn(),
  }
})
afterEach(() => { cleanup(); for (const k of Object.keys(seen)) delete seen[k] })

const show = (props = {}) => render(<Onboarding state={stateOf()} {...actions} {...props} />)
const heading = () => screen.getByRole('heading', { level: 1 })
const button = name => screen.queryByRole('button', { name })
const allow = () => fireEvent.click(screen.getByRole('button', { name: 'allow' }))
const flush = () => act(async () => { await Promise.resolve() })

describe('the onboarding shell', () => {
  it('lists the ten steps in order, with welcome, profile and finish not skippable', () => {
    expect(STEPS.map(s => s.id)).toEqual(IDS)
    expect(STEPS.filter(s => !s.skippable).map(s => s.id)).toEqual(['welcome', 'profile', 'finish'])
  })

  it('opens at the step the state names', () => {
    show({ state: stateOf({ currentStep: 'monomind' }) })
    expect(heading()).toHaveTextContent(i18n.t('onboarding.monomind.title'))
    expect(heading()).toHaveTextContent('Turn on monomind')
    expect(screen.getByTestId('step-monomind')).toBeInTheDocument()
    expect(screen.getByText('Step 5 of 10 · monomind')).toBeInTheDocument()
  })

  it('opens at startStep over the state', () => {
    show({ state: stateOf({ currentStep: 'monomind' }), startStep: 'browser' })
    expect(heading()).toHaveTextContent('Connect your browser')
  })

  it('hands every step its props', () => {
    show({ state: stateOf({ currentStep: 'about' }) })
    const p = seen.about
    expect(p.state.profileId).toBe('p1')
    expect(p.profileId).toBe('p1')
    expect(p.mode).toBe('full')
    for (const fn of ['reload', 'mark', 'skip', 'complete', 'onDone', 'onFinish', 'setCanContinue', 'setOnContinue', 'setContinueLabel', 'goTo']) {
      expect(typeof p[fn]).toBe('function')
    }
  })

  it('hides Skip on welcome, profile and finish', () => {
    for (const id of IDS) {
      const { unmount } = show({ startStep: id })
      const skippable = !['welcome', 'profile', 'finish'].includes(id)
      expect(button('Skip for now') !== null).toBe(skippable)
      unmount()
    }
  })

  it('keeps Continue disabled until the step allows it', () => {
    show({ startStep: 'system' })
    expect(button('Continue')).toBeDisabled()
    allow()
    expect(button('Continue')).toBeEnabled()
    fireEvent.click(button('Continue'))
    expect(heading()).toHaveTextContent('Create your profile')
    // the next step starts disallowed again
    expect(button('Continue')).toBeDisabled()
  })

  it('Skip calls skip(id) and moves to the next step', async () => {
    show({ startStep: 'system' })
    fireEvent.click(button('Skip for now'))
    await flush()
    expect(actions.skip).toHaveBeenCalledWith('system')
    expect(heading()).toHaveTextContent('Create your profile')
  })

  it('stays on the step and shows the error when skip fails', async () => {
    actions.skip = vi.fn(() => Promise.resolve({ ok: false, loginRequired: false, message: 'cannot write' }))
    show({ startStep: 'agents' })
    fireEvent.click(button('Skip for now'))
    await flush()
    expect(heading()).toHaveTextContent('Your agents')
    expect(screen.getByRole('alert')).toHaveTextContent('cannot write')
  })

  it('Back never goes before welcome', () => {
    show({ startStep: 'system' })
    fireEvent.click(button('Back'))
    expect(heading()).toHaveTextContent('Welcome')
    expect(button('Back')).toBeNull()
    fireEvent.keyDown(document.body, { key: 'Enter' })
    expect(heading()).toHaveTextContent('Welcome')
  })

  it('Enter continues only when Continue is enabled', () => {
    show({ startStep: 'system' })
    fireEvent.keyDown(document.body, { key: 'Enter' })
    expect(heading()).toHaveTextContent('System check')
    allow()
    fireEvent.keyDown(screen.getByLabelText('notes'), { key: 'Enter' })
    expect(heading()).toHaveTextContent('System check')
    fireEvent.keyDown(screen.getByLabelText('name'), { key: 'Enter', isComposing: true })
    expect(heading()).toHaveTextContent('System check')
    fireEvent.keyDown(screen.getByLabelText('name'), { key: 'Enter' })
    expect(heading()).toHaveTextContent('Create your profile')
  })

  it('Enter on a focused button does not also continue', () => {
    show({ startStep: 'agents' })
    allow()
    fireEvent.keyDown(button('Skip for now'), { key: 'Enter' })
    expect(heading()).toHaveTextContent('Your agents')
    expect(actions.skip).not.toHaveBeenCalled()
  })

  it('runs the step\'s onContinue first and stays when it resolves to false', async () => {
    show({ startStep: 'profile' })
    const handler = vi.fn(() => Promise.resolve(false))
    act(() => { seen.profile.setOnContinue(handler); seen.profile.setContinueLabel('Create profile') })
    allow()
    fireEvent.click(button('Create profile'))
    await flush()
    expect(handler).toHaveBeenCalledTimes(1)
    expect(heading()).toHaveTextContent('Create your profile')
    handler.mockImplementation(() => Promise.resolve(true))
    fireEvent.click(button('Create profile'))
    await flush()
    expect(heading()).toHaveTextContent('About you')
    expect(button('Continue')).toBeInTheDocument()
  })

  it('onDone advances, goTo jumps, and the last step finishes', () => {
    show({ startStep: 'firstRun' })
    act(() => seen.firstRun.goTo('agents'))
    expect(heading()).toHaveTextContent('Your agents')
    act(() => seen.agents.goTo('finish'))
    expect(heading()).toHaveTextContent("You're set")
    act(() => seen.finish.onDone({ page: 'orgs' }))
    expect(actions.onFinish).toHaveBeenCalledWith({ page: 'orgs' })
  })

  it('shows skipped steps as skipped in the rail', () => {
    const { container } = show({ state: stateOf({ currentStep: 'profile' }, { welcome: 'done', system: 'skipped' }) })
    const rail = screen.getByRole('navigation', { name: 'Setup progress' })
    expect(within(rail).getAllByRole('listitem')).toHaveLength(10)
    expect(container.querySelector('[data-step="system"]')).toHaveAttribute('data-status', 'skipped')
    expect(container.querySelector('[data-step="welcome"]')).toHaveAttribute('data-status', 'done')
    expect(container.querySelector('[data-step="profile"]')).toHaveAttribute('aria-current', 'step')
    expect(within(rail).getByRole('img', { name: 'Step 2, System check: skipped' })).toBeInTheDocument()
  })

  it('profile mode walks about, monomind and browser, all skippable, and finishes after browser', () => {
    show({ mode: 'profile', state: stateOf({ completed: true, currentStep: 'finish' }) })
    const rail = screen.getByRole('navigation', { name: 'Setup progress' })
    expect(within(rail).getAllByRole('listitem')).toHaveLength(3)
    expect(heading()).toHaveTextContent('About you')
    expect(button('Skip for now')).toBeInTheDocument()
    act(() => seen.about.goTo('browser'))
    allow()
    fireEvent.click(button('Continue'))
    expect(actions.onFinish).toHaveBeenCalledWith(undefined)
    expect(actions.complete).not.toHaveBeenCalled()
  })
})
