// @vitest-environment jsdom
// The dashboard's "Finish setting up" card: the steps left (skipped or not
// done) from the full state read, each reopening the onboarding there; gone
// when all are done or once hidden.
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, cleanup, within, waitFor, act } from '@testing-library/react'

const go = vi.hoisted(() => ({ GetOnboardingState: vi.fn(), GetOnboardingStateFull: vi.fn() }))
vi.mock('../../wailsjs/go/main/App', () => go)

import i18n from '../../i18n.js'
import { OnboardingContext } from '../../onboarding/onboardingContext.js'
import FinishSetupCard from './FinishSetupCard.jsx'
import { DISMISS_KEY } from './getStarted.js'

const IDS = ['welcome', 'system', 'profile', 'about', 'monomind', 'agents', 'browser', 'permissions', 'firstRun', 'finish']
const stateOf = (statuses = {}) => ({ completed: true, currentStep: 'finish', profileId: 'p1', steps: IDS.map(id => ({ id, status: statuses[id] || 'done' })) })

let ctx
const card = (props = {}) => <OnboardingContext.Provider value={ctx}><FinishSetupCard {...props} /></OnboardingContext.Provider>
const show = (state, props = {}) => {
  go.GetOnboardingStateFull.mockResolvedValue(state)
  // the gate's own (fast) state is never what the card lists
  ctx = { state: stateOf(), openOnboarding: vi.fn(() => Promise.resolve()), reload: vi.fn() }
  return render(card(props))
}
const settle = () => act(async () => {})

beforeEach(async () => {
  await i18n.changeLanguage('en')
  localStorage.clear()
  go.GetOnboardingState.mockReset()
  go.GetOnboardingStateFull.mockReset()
})
afterEach(() => { cleanup(); vi.restoreAllMocks() })

describe('FinishSetupCard', () => {
  it('lists only steps that are skipped or todo and opens onboarding at the clicked one', async () => {
    show(stateOf({ agents: 'skipped', browser: 'todo', finish: 'todo' }))
    const box = await screen.findByTestId('finish-setup')
    expect(go.GetOnboardingStateFull).toHaveBeenCalledTimes(1)
    expect(go.GetOnboardingState).not.toHaveBeenCalled()
    const rows = within(box).getAllByRole('listitem')
    expect(rows.map(r => r.textContent.replace(/Finish this$/, ''))).toEqual(['Your agentsskipped', 'Browsernot done'])
    expect(screen.getByText('7 of 9 done')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Finish this: Browser' }))
    expect(ctx.openOnboarding).toHaveBeenCalledWith('browser')
  })

  it('is not rendered when every step is done', async () => {
    show(stateOf())
    await settle()
    expect(go.GetOnboardingStateFull).toHaveBeenCalled()
    expect(screen.queryByTestId('finish-setup')).not.toBeInTheDocument()
  })

  it('is not rendered while or when the state cannot be read', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    go.GetOnboardingStateFull.mockRejectedValue('no profile selected')
    ctx = { state: null, openOnboarding: vi.fn(), reload: vi.fn() }
    render(card())
    expect(screen.queryByTestId('finish-setup')).not.toBeInTheDocument()
    await settle()
    expect(screen.queryByTestId('finish-setup')).not.toBeInTheDocument()
  })

  it('stays dismissed', async () => {
    const first = show(stateOf({ system: 'skipped' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Hide this card' }))
    expect(screen.queryByTestId('finish-setup')).not.toBeInTheDocument()
    expect(localStorage.getItem(DISMISS_KEY)).toBe('1')
    first.unmount()
    go.GetOnboardingStateFull.mockClear()
    show(stateOf({ system: 'skipped' }))
    await settle()
    expect(screen.queryByTestId('finish-setup')).not.toBeInTheDocument()
    expect(go.GetOnboardingStateFull).not.toHaveBeenCalled()
  })

  it('reads the full state again when the dashboard is shown again', async () => {
    const { rerender } = show(stateOf({ system: 'skipped' }), { isActive: true })
    await screen.findByTestId('finish-setup')
    go.GetOnboardingStateFull.mockResolvedValue(stateOf())
    rerender(card({ isActive: false }))
    rerender(card({ isActive: true }))
    await waitFor(() => expect(screen.queryByTestId('finish-setup')).not.toBeInTheDocument())
    expect(go.GetOnboardingStateFull).toHaveBeenCalledTimes(2)
  })
})
