// @vitest-environment jsdom
// The short setup for a later profile (mode 'profile'): about, monomind and
// browser only, every one skippable, over the finished village; the last
// button is Done and closes without completing anything. Steps are stubs.
import React from 'react'
import { describe, it, expect, vi, beforeAll, beforeEach, afterEach, afterAll } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, cleanup, act, within } from '@testing-library/react'
import i18n from '../i18n.js'
import Onboarding, { STEP_COMPONENTS } from './Onboarding.jsx'

const IDS = ['welcome', 'system', 'profile', 'about', 'monomind', 'agents', 'browser', 'permissions', 'firstRun', 'finish']
const original = { ...STEP_COMPONENTS }
const seen = {}
beforeAll(() => {
  for (const id of IDS) {
    STEP_COMPONENTS[id] = function StubStep(props) {
      seen[id] = props
      return <button type="button" onClick={() => props.setCanContinue(true)}>allow</button>
    }
  }
})
afterAll(() => { Object.assign(STEP_COMPONENTS, original) })

const stateOf = (statuses = {}, over = {}) => ({
  completed: true, currentStep: 'finish', profileId: 'p2',
  steps: IDS.map(id => ({ id, status: statuses[id] || (['about', 'monomind', 'agents', 'browser'].includes(id) ? 'todo' : 'done') })), ...over,
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

const show = (props = {}) => render(<Onboarding mode="profile" state={stateOf()} {...actions} {...props} />)
const heading = () => screen.getByRole('heading', { level: 1 })
const button = name => screen.queryByRole('button', { name })
const flush = () => act(async () => { await Promise.resolve() })

describe('the short setup for a later profile', () => {
  it('profile mode shows only about, monomind and browser', () => {
    const { container } = show()
    const rail = screen.getByRole('navigation', { name: 'Setup progress' })
    expect(within(rail).getAllByRole('listitem').map(li => li.getAttribute('data-step'))).toEqual(['about', 'monomind', 'browser'])
    expect(heading()).toHaveTextContent('About you')
    expect(screen.getByText('Step 1 of 3 · About you')).toBeInTheDocument()
    expect(screen.getByText('Set up this profile')).toBeInTheDocument()
    expect(seen.about.mode).toBe('profile')
    expect(seen.about.profileId).toBe('p2')
    // every one skippable
    const goTo = seen.about.goTo
    for (const id of ['about', 'monomind', 'browser']) {
      act(() => goTo(id))
      expect(heading()).toHaveTextContent({ about: 'About you', monomind: 'Turn on monomind', browser: 'Connect your browser' }[id])
      expect(button('Skip for now')).toBeInTheDocument()
    }
    act(() => goTo('about'))
    // over the finished village: every plot built, no sunrise
    const plots = [...container.querySelectorAll('.onb-vsvg .plot')]
    expect(plots.length).toBeGreaterThan(5)
    expect(plots.every(g => g.classList.contains('built'))).toBe(true)
    expect(container.querySelector('.onb-scene').classList.contains('dawn')).toBe(false)
  })

  it('opens at the first of the three not done yet', () => {
    show({ state: stateOf({ about: 'done' }) })
    expect(heading()).toHaveTextContent('Turn on monomind')
  })

  it('Done closes onboarding and does not call complete()', async () => {
    show({ startStep: 'browser' })
    expect(button('Continue')).toBeNull()
    expect(button('Done')).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: 'allow' }))
    fireEvent.click(button('Done'))
    await flush()
    expect(actions.onFinish).toHaveBeenCalledTimes(1)
    expect(actions.onFinish).toHaveBeenCalledWith(undefined)
    expect(actions.complete).not.toHaveBeenCalled()
  })

  it('skipping the last step also closes it, after writing the skip', async () => {
    show({ startStep: 'browser' })
    fireEvent.click(button('Skip for now'))
    await flush()
    expect(actions.skip).toHaveBeenCalledWith('browser')
    expect(actions.onFinish).toHaveBeenCalledTimes(1)
    expect(actions.complete).not.toHaveBeenCalled()
  })

  it('earlier steps keep Continue', () => {
    show()
    expect(button('Continue')).toBeInTheDocument()
    expect(button('Done')).toBeNull()
  })
})
