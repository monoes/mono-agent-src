// @vitest-environment jsdom
// The report card's Close mission button and the strip's Ready to close label.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import '../../../i18n.js'

const { sess, ask } = vi.hoisted(() => ({ sess: { show: vi.fn(), close: vi.fn(), pulse: vi.fn(), list: vi.fn(), board: vi.fn() }, ask: { ok: true, text: [] } }))
vi.mock('../../../services/api.js', () => ({ api: { openURL: vi.fn() }, notify: vi.fn() }))
vi.mock('../../../services/taskSessions.js', () => ({ sessionsApi: sess }))
vi.mock('../../ConfirmDialog.jsx', () => ({ confirm: (m) => { ask.text.push(m); return Promise.resolve(ask.ok) } }))
import { ReportCard } from './MissionCards.jsx'
import MissionStrip from './MissionStrip.jsx'
import { forgetConversationSessions } from './useMissionSession.js'

const doc = (state, extra = {}) => ({ id: 7, title: 'Mission 7', goal: 'g', state, archived: false, rev: 1, budget_usd: 5, spent_usd: 1, counts: { done: 2, total: 2 }, conversation_id: 'c1', ...extra })
const card = (payload, conv = 'c1') => render(<ReportCard item={{ type: 'report', id: 'r', payload: { sessionId: 7, report: 'All done.', ...payload } }} conversationId={conv} onNavigate={() => {}} />)

beforeEach(() => {
  vi.clearAllMocks()
  forgetConversationSessions()
  ask.ok = true; ask.text = []
  sess.pulse.mockResolvedValue({})
  sess.board.mockResolvedValue({ tasks: {} })
})
afterEach(cleanup)

describe('report card close button', () => {
  it.each(['running', 'paused'])('shows the hint and a primary button when closing was proposed and the live session is %s', async (state) => {
    sess.show.mockResolvedValue({ session: doc(state) })
    card({ closeProposed: true })
    const btn = await screen.findByRole('button', { name: 'Close mission' })
    expect(btn).toHaveClass('btn-primary')
    expect(screen.getByText('The mission lead proposed closing it.')).toBeInTheDocument()
  })
  it('confirms, then closes the live session id and shows Closed after the reload', async () => {
    sess.show.mockResolvedValueOnce({ session: doc('running') }).mockResolvedValue({ session: doc('done') })
    sess.close.mockResolvedValue({ session: doc('done') })
    card({ closeProposed: true })
    fireEvent.click(await screen.findByRole('button', { name: 'Close mission' }))
    await waitFor(() => expect(sess.close).toHaveBeenCalledWith(7))
    expect(ask.text).toEqual(['Close this mission? Its tasks stay as they are; workers stop starting.'])
    expect(await screen.findByText('Closed')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Close mission' })).toBeNull()
  })
  it('does not close when the operator declines', async () => {
    ask.ok = false
    sess.show.mockResolvedValue({ session: doc('running') })
    card({ closeProposed: true })
    fireEvent.click(await screen.findByRole('button', { name: 'Close mission' }))
    await waitFor(() => expect(ask.text).toHaveLength(1))
    expect(sess.close).not.toHaveBeenCalled()
  })
  it('shows the CLI refusal', async () => {
    sess.show.mockResolvedValue({ session: doc('running') })
    sess.close.mockResolvedValue({ error: 'only the operator may close a mission', code: 'operator_only' })
    card({ closeProposed: true })
    fireEvent.click(await screen.findByRole('button', { name: 'Close mission' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('only the operator may close a mission')
  })
  it('reads Closed, with no button, when the live session is already done', async () => {
    sess.show.mockResolvedValue({ session: doc('done') })
    card({ closeProposed: true })
    expect(await screen.findByText('Closed')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Close mission' })).toBeNull()
  })
  it.each(['stopped', 'draft', 'failed'])('shows nothing to close for a %s session', async (state) => {
    sess.show.mockResolvedValue({ session: doc(state) })
    card({ closeProposed: true })
    await waitFor(() => expect(sess.show).toHaveBeenCalled())
    await screen.findByTestId('mission-report')
    expect(screen.queryByRole('button', { name: 'Close mission' })).toBeNull()
    expect(screen.queryByText('Closed')).toBeNull()
  })
  it('shows no button when closing was not proposed, and reads nothing', async () => {
    sess.show.mockResolvedValue({ session: doc('running') })
    card({})
    await screen.findByTestId('mission-report')
    expect(screen.queryByRole('button', { name: 'Close mission' })).toBeNull()
    expect(sess.show).not.toHaveBeenCalled()
  })
  it('shows nothing for a forged payload pointing at another conversation\'s session', async () => {
    sess.show.mockResolvedValue({ session: doc('running', { conversation_id: 'other' }) })
    card({ closeProposed: true })
    await waitFor(() => expect(sess.show).toHaveBeenCalled())
    expect(screen.queryByRole('button', { name: 'Close mission' })).toBeNull()
    expect(screen.queryByText('The mission lead proposed closing it.')).toBeNull()
    expect(screen.queryByText('Closed')).toBeNull()
  })
})

describe('mission strip label', () => {
  const strip = (closeProposed) => render(<MissionStrip conversationId="c1" closeProposed={closeProposed} />)
  beforeEach(() => { sess.list.mockResolvedValue({ sessions: [doc('running')] }) })
  it('says Ready to close, not Running, once the lead proposed closing', async () => {
    sess.show.mockResolvedValue({ session: doc('running') })
    strip([7])
    const s = await screen.findByTestId('mission-strip')
    await waitFor(() => expect(screen.getByTestId('ts-state')).toHaveTextContent('Ready to close'))
    expect(s).not.toHaveTextContent('Running')
  })
  it('says Running when the proposal is for another session or absent', async () => {
    sess.show.mockResolvedValue({ session: doc('running') })
    strip([9])
    await screen.findByTestId('mission-strip')
    expect(screen.getByTestId('ts-state')).toHaveTextContent('Running')
  })
  it('reads Done once the session is closed', async () => {
    sess.list.mockResolvedValue({ sessions: [doc('done')] })
    sess.show.mockResolvedValue({ session: doc('done') })
    render(<MissionStrip conversationId="c1" reported closeProposed={[7]} />)
    await waitFor(() => expect(screen.getByTestId('ts-state')).toHaveTextContent('Done'))
  })
})
