// @vitest-environment jsdom
// The report link of a chat (navData {sessionId, report}) and a hinted session's first paint.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import '../../i18n.js'

const { api, sess } = vi.hoisted(() => ({
  api: { board: vi.fn(), show: vi.fn(), add: vi.fn(), edit: vi.fn(), move: vi.fn(), approve: vi.fn(), archive: vi.fn(), comment: vi.fn(), agentShell: vi.fn() },
  sess: { list: vi.fn(), show: vi.fn(), plan: vi.fn(), board: vi.fn(), approve: vi.fn(), pause: vi.fn(), resume: vi.fn(), stop: vi.fn(), archive: vi.fn(), budget: vi.fn(), pulse: vi.fn() },
}))
vi.mock('../../services/tasks.js', () => ({ tasksApi: api, onTasksChanged: () => () => {} }))
vi.mock('../../services/taskSessions.js', () => ({ sessionsApi: sess }))
import Tasks from '../../pages/Tasks.jsx'

const card = (id, extra = {}) => ({ id, title: `Task ${id}`, notes: '', position: id, claim: null, session_id: 1, ...extra })
const ses = (id, state = 'done') => ({ id, title: `Mission ${id}`, goal: 'g', state, archived: false, rev: 1, budget_usd: 5, spent_usd: 1, counts: { done: 3, total: 3 } })
const integ = card(33, { kind: 'check', title: 'Integration check', status: 'done' })
const boardOf = (id) => ({ rev: 1, counts: {}, tasks: id === 1 ? { done: [card(31), card(32), integ] } : { done: [card(41, { session_id: 2, title: 'Other task', kind: 'check' })] } })
const finalReport = { id: 9, task_id: 33, kind: 'report', ref: 'final-report', note: 'All checks passed.' }
let withReport

beforeEach(() => {
  Object.values(api).forEach(f => f.mockReset())
  Object.values(sess).forEach(f => f.mockReset())
  withReport = true
  api.board.mockResolvedValue({ rev: 1, counts: {}, tasks: { inbox: [{ id: 1, title: 'Personal task', notes: '', position: 1, claim: null }] } })
  api.agentShell.mockResolvedValue('')
  api.show.mockImplementation(async (id) => ({ task: card(id, id === 33 ? integ : {}), events: [], artifacts: id === 33 && withReport ? [finalReport] : [] }))
  sess.list.mockResolvedValue({ sessions: [ses(1), ses(2)] })
  sess.board.mockImplementation(async (id) => ({ ...boardOf(id), session: ses(id) }))
  sess.show.mockResolvedValue({ session: ses(1), events: [] })
  sess.plan.mockImplementation(async (id) => ({ tasks: id === 1 ? [card(31), card(32), integ] : [card(41, { session_id: 2, kind: 'check', title: 'Unit checks' })] }))
  sess.pulse.mockResolvedValue({ rev: 1, state: 'done' })
})
afterEach(cleanup)

describe('report hint', () => {
  it('opens the session and the drawer of the task holding the final report, at its artifacts', async () => {
    render(<Tasks navData={{ sessionId: 1, report: true }} />)
    const drawer = await screen.findByRole('dialog', { name: 'Integration check' })
    expect(await screen.findByTestId('artifacts')).toHaveTextContent('final-report')
    expect(drawer).toHaveTextContent('All checks passed.')
    await waitFor(() => expect(screen.getByTestId('artifacts-head')).toHaveFocus())
    expect(screen.getByRole('region', { name: 'Session Mission 1' })).toBeInTheDocument()
  })
  it('opens the Integration check drawer even without a report artifact yet', async () => {
    withReport = false
    render(<Tasks navData={{ sessionId: 1, report: true }} />)
    expect(await screen.findByRole('dialog', { name: 'Integration check' })).toBeInTheDocument()
  })
  it('opens only the session when it has no integration task', async () => {
    render(<Tasks navData={{ sessionId: 2, report: true }} />)
    expect(await screen.findByRole('region', { name: 'Session Mission 2' })).toBeInTheDocument()
    await screen.findByText('Other task')
    await waitFor(() => expect(sess.plan).toHaveBeenCalledWith(2))
    expect(screen.queryByRole('dialog')).toBeNull()
  })
  it('opens only the session for a hint without report', async () => {
    render(<Tasks navData={{ sessionId: 1 }} />)
    await screen.findByText('Task 31')
    expect(sess.plan).not.toHaveBeenCalled()
    expect(screen.queryByRole('dialog')).toBeNull()
  })
  it('does not open the hinted session\'s task in another session the person switched to', async () => {
    render(<Tasks navData={{ sessionId: 1, report: true }} />)
    await screen.findByRole('dialog', { name: 'Integration check' })
    fireEvent.click(await screen.findByRole('tab', { name: /Mission 2/ }))
    await screen.findByText('Other task')
    expect(screen.queryByRole('dialog', { name: 'Integration check' })).toBeNull()
    expect(screen.queryByRole('dialog')).toBeNull()
  })
})

describe('first paint of a hinted session', () => {
  it('holds the hint while the list loads and never shows Personal', async () => {
    let release
    sess.list.mockImplementation(() => new Promise(r => { release = () => r({ sessions: [ses(1), ses(2)] }) }))
    render(<Tasks navData={{ sessionId: 1 }} />)
    expect(screen.getByTestId('ts-loading')).toBeInTheDocument()
    await new Promise(r => setTimeout(r, 30))
    expect(screen.queryByText('Personal task')).toBeNull()
    expect(api.board).not.toHaveBeenCalled()
    release()
    expect(await screen.findByRole('region', { name: 'Session Mission 1' })).toBeInTheDocument()
    expect(screen.queryByText('Personal task')).toBeNull()
  })
  it('falls back to Personal when the hinted session is not in the list', async () => {
    sess.list.mockResolvedValue({ sessions: [] })
    render(<Tasks navData={{ sessionId: 99 }} />)
    expect(await screen.findByText('Personal task')).toBeInTheDocument()
  })
})
