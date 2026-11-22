// @vitest-environment jsdom
// The dashboard's "Finish setting up" card: the real gaps (skipped or not done,
// never Welcome or You're set) from one full state read per app session, each
// row reopening the onboarding there; nothing when no row is left or once hidden.
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
import { resetSetupSession } from './setupSession.js'

const IDS = ['welcome', 'system', 'profile', 'about', 'monomind', 'agents', 'browser', 'permissions', 'firstRun', 'finish']
const stateOf = (statuses = {}) => ({ completed: true, currentStep: 'finish', profileId: 'p1', steps: IDS.map(id => ({ id, status: statuses[id] || 'done' })) })

let ctx
const card = (props = {}) => <OnboardingContext.Provider value={ctx}><FinishSetupCard {...props} /></OnboardingContext.Provider>
const show = (state, props = {}) => {
  if (state !== undefined) go.GetOnboardingStateFull.mockResolvedValue(state)
  return render(card(props))
}
const settle = () => act(async () => {})

beforeEach(async () => {
  await i18n.changeLanguage('en')
  localStorage.clear()
  resetSetupSession()
  go.GetOnboardingState.mockReset()
  go.GetOnboardingStateFull.mockReset()
  ctx = { state: null, openOnboarding: vi.fn(() => Promise.resolve()), reload: vi.fn(), loadFull: vi.fn() }
})
afterEach(() => { cleanup(); vi.restoreAllMocks() })

describe('FinishSetupCard', () => {
  it('lists only steps that are skipped or todo and opens onboarding at the clicked one', async () => {
    show(stateOf({ agents: 'skipped', browser: 'todo', welcome: 'todo', finish: 'todo' }))
    const box = await screen.findByTestId('finish-setup')
    const rows = within(box).getAllByRole('listitem')
    // Welcome and You're set are never rows
    expect(rows.map(r => r.textContent.replace(/Finish this$/, ''))).toEqual(['Your agentsskipped', 'Browsernot done'])
    expect(screen.getByText('6 of 8 done')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Finish this: Browser' }))
    expect(ctx.openOnboarding).toHaveBeenCalledWith('browser')
    // the read stays local: the gate's state is neither read nor switched to full reads
    expect(go.GetOnboardingState).not.toHaveBeenCalled()
    expect(ctx.loadFull).not.toHaveBeenCalled()
  })

  it('reads once across repeated dashboard shows and scope switches', async () => {
    const { rerender, unmount } = show(stateOf({ system: 'skipped' }), { isActive: true })
    await screen.findByTestId('finish-setup')
    rerender(card({ isActive: false }))
    rerender(card({ isActive: true }))
    rerender(card({ isActive: false }))
    rerender(card({ isActive: true }))
    unmount() // a scope switch remounts the card
    show(undefined)
    expect(await screen.findByTestId('finish-setup')).toBeInTheDocument()
    expect(go.GetOnboardingStateFull).toHaveBeenCalledTimes(1)
  })

  it('does not read again once a read left nothing to do', async () => {
    const first = show(stateOf())
    await settle()
    expect(screen.queryByTestId('finish-setup')).not.toBeInTheDocument()
    first.unmount()
    show(undefined, { isActive: true })
    await settle()
    expect(screen.queryByTestId('finish-setup')).not.toBeInTheDocument()
    expect(go.GetOnboardingStateFull).toHaveBeenCalledTimes(1)
  })

  it('is not rendered when only Welcome is left', async () => {
    show(stateOf({ welcome: 'todo' }))
    await settle()
    expect(go.GetOnboardingStateFull).toHaveBeenCalledTimes(1)
    expect(screen.queryByTestId('finish-setup')).not.toBeInTheDocument()
  })

  it('is not rendered when the full state cannot be read', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    go.GetOnboardingStateFull.mockRejectedValue('no profile selected')
    show(undefined)
    await settle()
    expect(screen.queryByTestId('finish-setup')).not.toBeInTheDocument()
  })

  it('does not read when dismissed, and stays dismissed', async () => {
    const first = show(stateOf({ system: 'skipped' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Hide this card' }))
    expect(screen.queryByTestId('finish-setup')).not.toBeInTheDocument()
    expect(localStorage.getItem(DISMISS_KEY)).toBe('1')
    first.unmount()
    resetSetupSession() // a new app session
    go.GetOnboardingStateFull.mockClear()
    show(stateOf({ system: 'skipped' }))
    await settle()
    expect(screen.queryByTestId('finish-setup')).not.toBeInTheDocument()
    expect(go.GetOnboardingStateFull).not.toHaveBeenCalled()
  })

  it('reads again after the onboarding was opened from it and closed', async () => {
    const first = show(stateOf({ browser: 'skipped' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Finish this: Browser' }))
    await settle()
    first.unmount() // the app goes away while the onboarding shows
    go.GetOnboardingStateFull.mockResolvedValue(stateOf())
    show(undefined) // the onboarding closed; the app is back
    await settle()
    expect(go.GetOnboardingStateFull).toHaveBeenCalledTimes(2)
    expect(screen.queryByTestId('finish-setup')).not.toBeInTheDocument()
  })

  it('reads again on its own refresh', async () => {
    show(stateOf({ about: 'todo', agents: 'todo' }))
    await screen.findByText('About you')
    go.GetOnboardingStateFull.mockResolvedValue(stateOf({ agents: 'todo' }))
    fireEvent.click(screen.getByRole('button', { name: 'Check again' }))
    await waitFor(() => expect(screen.queryByText('About you')).not.toBeInTheDocument())
    expect(screen.getByText('Your agents')).toBeInTheDocument()
    expect(go.GetOnboardingStateFull).toHaveBeenCalledTimes(2)
  })
})
