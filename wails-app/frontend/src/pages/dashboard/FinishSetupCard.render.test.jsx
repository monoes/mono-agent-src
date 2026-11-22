// @vitest-environment jsdom
// The dashboard's "Finish setting up" card: the steps left (skipped or not
// done), each reopening the onboarding there; gone when all are done or once
// hidden.
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, cleanup, within } from '@testing-library/react'
import i18n from '../../i18n.js'
import { OnboardingContext } from '../../onboarding/onboardingContext.js'
import FinishSetupCard from './FinishSetupCard.jsx'
import { DISMISS_KEY } from './getStarted.js'

const IDS = ['welcome', 'system', 'profile', 'about', 'monomind', 'agents', 'browser', 'permissions', 'firstRun', 'finish']
const stateOf = (statuses = {}) => ({ completed: true, currentStep: 'finish', profileId: 'p1', steps: IDS.map(id => ({ id, status: statuses[id] || 'done' })) })

let ctx
const show = (state, props = {}) => {
  ctx = { state, openOnboarding: vi.fn(() => Promise.resolve()), reload: vi.fn(() => Promise.resolve({ ok: true })) }
  return render(<OnboardingContext.Provider value={ctx}><FinishSetupCard {...props} /></OnboardingContext.Provider>)
}

beforeEach(async () => {
  await i18n.changeLanguage('en')
  localStorage.clear()
})
afterEach(() => { cleanup(); vi.restoreAllMocks() })

describe('FinishSetupCard', () => {
  it('lists only steps that are skipped or todo and opens onboarding at the clicked one', () => {
    show(stateOf({ agents: 'skipped', browser: 'todo', finish: 'todo' }))
    const rows = within(screen.getByTestId('finish-setup')).getAllByRole('listitem')
    expect(rows.map(r => r.textContent.replace(/Finish this$/, ''))).toEqual(['Your agentsskipped', 'Browsernot done'])
    expect(screen.getByText('7 of 9 done')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Finish this: Browser' }))
    expect(ctx.openOnboarding).toHaveBeenCalledWith('browser')
  })

  it('is not rendered when every step is done', () => {
    show(stateOf())
    expect(screen.queryByTestId('finish-setup')).not.toBeInTheDocument()
  })

  it('is not rendered before the state is known', () => {
    show(null)
    expect(screen.queryByTestId('finish-setup')).not.toBeInTheDocument()
  })

  it('stays dismissed', () => {
    const first = show(stateOf({ system: 'skipped' }))
    fireEvent.click(screen.getByRole('button', { name: 'Hide this card' }))
    expect(screen.queryByTestId('finish-setup')).not.toBeInTheDocument()
    expect(localStorage.getItem(DISMISS_KEY)).toBe('1')
    first.unmount()
    show(stateOf({ system: 'skipped' }))
    expect(screen.queryByTestId('finish-setup')).not.toBeInTheDocument()
  })

  it('reads the state again when the dashboard is shown again', () => {
    const { rerender } = show(stateOf({ system: 'skipped' }), { isActive: true })
    expect(ctx.reload).not.toHaveBeenCalled()
    rerender(<OnboardingContext.Provider value={ctx}><FinishSetupCard isActive={false} /></OnboardingContext.Provider>)
    rerender(<OnboardingContext.Provider value={ctx}><FinishSetupCard isActive /></OnboardingContext.Provider>)
    expect(ctx.reload).toHaveBeenCalledTimes(1)
  })
})
