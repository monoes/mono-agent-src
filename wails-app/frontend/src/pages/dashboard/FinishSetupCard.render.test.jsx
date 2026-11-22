// @vitest-environment jsdom
// The dashboard's "Finish setting up" card: the real gaps (skipped or not done,
// never Welcome or You're set) from the gate's state after loadFull(), each
// reopening the onboarding there; nothing when no row is left or once hidden.
import React, { useMemo, useState } from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, cleanup, within, waitFor, act } from '@testing-library/react'
import i18n from '../../i18n.js'
import { OnboardingContext } from '../../onboarding/onboardingContext.js'
import FinishSetupCard from './FinishSetupCard.jsx'
import { DISMISS_KEY } from './getStarted.js'

const IDS = ['welcome', 'system', 'profile', 'about', 'monomind', 'agents', 'browser', 'permissions', 'firstRun', 'finish']
const stateOf = (statuses = {}) => ({ completed: true, currentStep: 'finish', profileId: 'p1', steps: IDS.map(id => ({ id, status: statuses[id] || 'done' })) })
// the gate's first, fast read on a completed install: marks only, every fact step todo
const FAST = stateOf({ welcome: 'todo', system: 'todo', about: 'todo', monomind: 'todo', agents: 'todo', browser: 'todo', permissions: 'todo', firstRun: 'todo' })

let gate
// A stand-in gate: loadFull() reads the full state into the context's state.
function Gate({ fast = FAST, full, fail = false, pending = false, props = {} }) {
  const [state, setState] = useState(fast)
  gate.setFull = (s) => { full = s }
  const ctx = useMemo(() => ({
    state,
    openOnboarding: gate.openOnboarding,
    reload: vi.fn(),
    loadFull: gate.loadFull.mockImplementation(async () => {
      if (pending) return new Promise(() => {})
      if (fail) return { ok: false, loginRequired: false, message: 'no profile selected' }
      setState(full)
      return { ok: true, value: full }
    }),
  }), [state]) // eslint-disable-line react-hooks/exhaustive-deps
  return <OnboardingContext.Provider value={ctx}><FinishSetupCard {...props} /></OnboardingContext.Provider>
}
const show = (full, opts = {}) => render(<Gate full={full} {...opts} />)

beforeEach(async () => {
  await i18n.changeLanguage('en')
  localStorage.clear()
  gate = { openOnboarding: vi.fn(() => Promise.resolve()), loadFull: vi.fn() }
})
afterEach(() => { cleanup(); vi.restoreAllMocks() })

describe('FinishSetupCard', () => {
  it('lists only steps that are skipped or todo and opens onboarding at the clicked one', async () => {
    show(stateOf({ agents: 'skipped', browser: 'todo', welcome: 'todo', finish: 'todo' }))
    const box = await screen.findByTestId('finish-setup')
    expect(gate.loadFull).toHaveBeenCalledTimes(1)
    const rows = within(box).getAllByRole('listitem')
    // Welcome and You're set are never rows
    expect(rows.map(r => r.textContent.replace(/Finish this$/, ''))).toEqual(['Your agentsskipped', 'Browsernot done'])
    expect(screen.getByText('6 of 8 done')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Finish this: Browser' }))
    expect(gate.openOnboarding).toHaveBeenCalledWith('browser')
  })

  it('never lists from the fast read', async () => {
    // while loadFull is pending, the fast state (every fact step todo) shows nothing
    show(stateOf(), { pending: true })
    await act(async () => {})
    expect(gate.loadFull).toHaveBeenCalledTimes(1)
    expect(screen.queryByTestId('finish-setup')).not.toBeInTheDocument()
  })

  it('is not rendered when every step is done', async () => {
    show(stateOf())
    await waitFor(() => expect(gate.loadFull).toHaveBeenCalled())
    await act(async () => {})
    expect(screen.queryByTestId('finish-setup')).not.toBeInTheDocument()
  })

  it('is not rendered when only Welcome is left', async () => {
    show(stateOf({ welcome: 'todo' }))
    await waitFor(() => expect(gate.loadFull).toHaveBeenCalled())
    await act(async () => {})
    expect(screen.queryByTestId('finish-setup')).not.toBeInTheDocument()
  })

  it('is not rendered when the full state cannot be read', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    show(stateOf({ agents: 'todo' }), { fail: true })
    await waitFor(() => expect(gate.loadFull).toHaveBeenCalled())
    await act(async () => {})
    expect(screen.queryByTestId('finish-setup')).not.toBeInTheDocument()
  })

  it('stays dismissed', async () => {
    const first = show(stateOf({ system: 'skipped' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Hide this card' }))
    expect(screen.queryByTestId('finish-setup')).not.toBeInTheDocument()
    expect(localStorage.getItem(DISMISS_KEY)).toBe('1')
    first.unmount()
    gate.loadFull.mockClear()
    show(stateOf({ system: 'skipped' }))
    await act(async () => {})
    expect(screen.queryByTestId('finish-setup')).not.toBeInTheDocument()
    expect(gate.loadFull).not.toHaveBeenCalled()
  })

  it('reads the full state again when the dashboard is shown again', async () => {
    const { rerender } = render(<Gate full={stateOf({ system: 'skipped' })} props={{ isActive: true }} />)
    await screen.findByTestId('finish-setup')
    gate.setFull(stateOf())
    rerender(<Gate full={stateOf()} props={{ isActive: false }} />)
    rerender(<Gate full={stateOf()} props={{ isActive: true }} />)
    await waitFor(() => expect(screen.queryByTestId('finish-setup')).not.toBeInTheDocument())
    expect(gate.loadFull).toHaveBeenCalledTimes(2)
  })
})
