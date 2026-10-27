// @vitest-environment jsdom
// Close mission: the operator's button on a session header (the lead only proposes closing).
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import '../../i18n.js'

const ask = vi.hoisted(() => ({ ok: true, text: [] }))
vi.mock('../ConfirmDialog.jsx', () => ({ confirm: (m) => { ask.text.push(m); return Promise.resolve(ask.ok) } }))
vi.mock('../../services/taskSessions.js', () => ({ sessionsApi: {} }))
import SessionHeader from './SessionHeader.jsx'

const sess = (state, extra = {}) => ({ id: 7, title: 'Mission 7', goal: 'g', state, archived: false, rev: 1, budget_usd: 0, spent_usd: 0, counts: { done: 1, total: 2 }, ...extra })
const head = (session, props = {}) => render(<SessionHeader session={session} view="board" onView={() => {}} onAct={props.onAct || vi.fn()} onBudget={() => {}} notice={props.notice} onClearNotice={() => {}} />)

beforeEach(() => { ask.ok = true; ask.text = [] })
afterEach(cleanup)

describe('Close mission on the session header', () => {
  it.each(['running', 'paused'])('is offered for a %s session', (state) => {
    head(sess(state))
    expect(screen.getByRole('button', { name: 'Close mission' })).toBeInTheDocument()
  })
  it.each(['draft', 'done', 'stopped', 'failed'])('is not offered for a %s session', (state) => {
    head(sess(state))
    expect(screen.queryByRole('button', { name: 'Close mission' })).toBeNull()
  })
  it('is not offered for an archived session', () => {
    head(sess('running', { archived: true }))
    expect(screen.queryByRole('button', { name: 'Close mission' })).toBeNull()
  })
  it('asks first, then closes the shown session', async () => {
    const onAct = vi.fn().mockResolvedValue({})
    head(sess('running'), { onAct })
    fireEvent.click(screen.getByRole('button', { name: 'Close mission' }))
    await waitFor(() => expect(onAct).toHaveBeenCalledWith('close', 7))
    expect(ask.text).toEqual(['Close this mission? Its tasks stay as they are; workers stop starting.'])
  })
  it('does nothing when the operator declines', async () => {
    ask.ok = false
    const onAct = vi.fn()
    head(sess('paused'), { onAct })
    fireEvent.click(screen.getByRole('button', { name: 'Close mission' }))
    await waitFor(() => expect(ask.text).toHaveLength(1))
    expect(onAct).not.toHaveBeenCalled()
  })
  it('shows the CLI refusal message', () => {
    head(sess('running'), { notice: { error: 'only the operator may close a mission', code: 'operator_only' } })
    expect(screen.getByRole('alert')).toHaveTextContent('only the operator may close a mission')
  })
})
