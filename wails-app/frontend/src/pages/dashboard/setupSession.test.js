// The setup card's session answer: a read that was under way when the answer
// went stale never makes it stand again, and monomind finishing its init (from
// any page) makes it stale.
import { describe, it, expect, vi, beforeEach } from 'vitest'

const go = vi.hoisted(() => ({ GetOnboardingStateFull: vi.fn() }))
vi.mock('../../wailsjs/go/main/App', () => go)
const events = vi.hoisted(() => ({ handlers: {} }))
vi.mock('../../services/api.js', () => ({
  subscribeEvent: vi.fn((name, fn) => { events.handlers[name] = fn; return () => {} }),
}))

import { setupSnapshot, readSetupState, markSetupStale, resetSetupSession } from './setupSession.js'

const stateOf = (tag) => ({ completed: true, steps: [], tag })

beforeEach(() => {
  resetSetupSession()
  events.handlers = {}
  go.GetOnboardingStateFull.mockReset()
})

describe('setup session', () => {
  it('stores a read as the standing answer', async () => {
    go.GetOnboardingStateFull.mockResolvedValue(stateOf('a'))
    await readSetupState()
    expect(setupSnapshot()).toEqual({ state: stateOf('a'), fresh: true })
  })

  it('a read under way when the answer goes stale does not make it stand', async () => {
    let answer
    go.GetOnboardingStateFull.mockImplementationOnce(() => new Promise(r => { answer = r }))
    const old = readSetupState()
    markSetupStale() // e.g. the onboarding closed while the read was out
    answer(stateOf('old'))
    await old
    expect(setupSnapshot().fresh).toBe(false)
    // the next read is a new one, and it stands
    go.GetOnboardingStateFull.mockResolvedValue(stateOf('new'))
    await readSetupState()
    expect(go.GetOnboardingStateFull).toHaveBeenCalledTimes(2)
    expect(setupSnapshot()).toEqual({ state: stateOf('new'), fresh: true })
  })

  it('an older read answering last never replaces a newer one', async () => {
    let answerOld
    go.GetOnboardingStateFull.mockImplementationOnce(() => new Promise(r => { answerOld = r }))
    const old = readSetupState()
    markSetupStale()
    go.GetOnboardingStateFull.mockResolvedValue(stateOf('new'))
    await readSetupState()
    answerOld(stateOf('old'))
    await old
    expect(setupSnapshot()).toEqual({ state: stateOf('new'), fresh: true })
  })

  it('reads share one call while nothing changed', async () => {
    go.GetOnboardingStateFull.mockResolvedValue(stateOf('a'))
    await Promise.all([readSetupState(), readSetupState()])
    expect(go.GetOnboardingStateFull).toHaveBeenCalledTimes(1)
  })

  it('goes stale when monomind finishes turning on, not on its progress lines', async () => {
    go.GetOnboardingStateFull.mockResolvedValue(stateOf('a'))
    await readSetupState()
    const onInit = events.handlers['monomind:initProgress']
    expect(onInit).toBeTypeOf('function')
    onInit({ kind: 'line', message: 'installing' })
    onInit({ kind: 'error', message: 'failed' })
    expect(setupSnapshot().fresh).toBe(true)
    onInit({ kind: 'done', message: '' })
    expect(setupSnapshot().fresh).toBe(false)
  })
})
