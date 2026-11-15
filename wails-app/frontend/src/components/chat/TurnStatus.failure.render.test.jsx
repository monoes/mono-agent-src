// @vitest-environment jsdom
import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import { TurnStatus } from './TurnStatus.jsx'
import { initialChatState } from './chatReducer.js'
import { failureHint } from '../../lib/turnFailure.js'

const failed = (terminal, extra = {}) => ({ ...initialChatState(), terminal: { status: 'failed', exitCode: null, historySaved: false, ...terminal }, ...extra })

afterEach(cleanup)

describe('a failed turn says why', () => {
  it('shows the reason in plain text next to the Failed label', () => {
    render(<TurnStatus state={failed({ reason: 'the coder host could not start the turn' })} stopRequested={false} />)
    expect(screen.getByText('Failed')).toBeTruthy()
    expect(screen.getByText(/the coder host could not start the turn/)).toBeTruthy()
  })

  it('adds what to do when the code has a known remedy', () => {
    render(<TurnStatus state={failed({ reason: 'claude is not logged in', code: 'agent_not_setup' })} stopRequested={false} />)
    expect(screen.getByText(/claude is not logged in/)).toBeTruthy()
    expect(screen.getByTestId('turn-failure-hint').textContent).toMatch(/AI agents/)
  })

  it('shows the raw reason and no hint for an unknown code', () => {
    render(<TurnStatus state={failed({ reason: 'weird thing', code: 'nope_unknown' })} stopRequested={false} />)
    expect(screen.getByText(/weird thing/)).toBeTruthy()
    expect(screen.queryByTestId('turn-failure-hint')).toBeNull()
    expect(failureHint('nope_unknown')).toBe('')
  })

  it('does not say it twice when the turn already shows the same text as a notice', () => {
    const state = failed({ reason: 'refused' }, { notices: [{ code: 'turn.failed', message: 'refused', severity: 'error' }] })
    render(<TurnStatus state={state} stopRequested={false} />)
    expect(screen.queryByText(/refused/)).toBeNull()
  })

  it('shows no reason for a completed turn', () => {
    render(<TurnStatus state={{ ...initialChatState(), terminal: { status: 'completed', reason: 'end_turn', exitCode: 0, historySaved: true } }} stopRequested={false} />)
    expect(screen.queryByText(/end_turn/)).toBeNull()
  })
})

describe('a forged or oversized failure', () => {
  it('shows no hint for a code that names an Object property, and does not crash', () => {
    for (const code of ['constructor', 'toString', '__proto__', 'hasOwnProperty']) {
      expect(failureHint(code)).toBe('')
    }
    render(<TurnStatus state={failed({ reason: 'x', code: 'constructor' })} stopRequested={false} />)
    expect(screen.queryByTestId('turn-failure-hint')).toBeNull()
  })

  it('strips control and bidi characters and cuts a very long reason', () => {
    const long = 'a'.repeat(2000)
    render(<TurnStatus state={failed({ reason: `bad‮reason\u001b[31m ${long}` })} stopRequested={false} />)
    const text = screen.getByRole('status').textContent
    expect(text).not.toMatch(/[‮\u001b]/)
    expect(text.length).toBeLessThan(700)
    expect(text).toMatch(/…/)
  })
})
