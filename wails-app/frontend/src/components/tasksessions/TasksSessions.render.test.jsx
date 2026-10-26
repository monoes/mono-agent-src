// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import '../../i18n.js'

const { api, sess, ask } = vi.hoisted(() => ({
  api: { board: vi.fn(), show: vi.fn(), add: vi.fn(), edit: vi.fn(), move: vi.fn(), approve: vi.fn(), archive: vi.fn(), comment: vi.fn(), agentShell: vi.fn() },
  sess: { list: vi.fn(), show: vi.fn(), plan: vi.fn(), board: vi.fn(), approve: vi.fn(), pause: vi.fn(), resume: vi.fn(), stop: vi.fn(), archive: vi.fn(), budget: vi.fn(), pulse: vi.fn() },
  ask: { answer: true },
}))
vi.mock('../../services/tasks.js', () => ({ tasksApi: api, onTasksChanged: () => () => {} }))
vi.mock('../../services/taskSessions.js', () => ({ sessionsApi: sess }))
vi.mock('../ConfirmDialog.jsx', () => ({ confirm: () => Promise.resolve(ask.answer) }))
import Tasks from '../../pages/Tasks.jsx'
import { CoderBubbleContext } from '../bubbles/CoderBubbleContext.jsx'

const card = (id, extra = {}) => ({ id, title: `Task ${id}`, notes: '', position: id, claim: null, ...extra })
const session = (id, state, extra = {}) => ({
  id, title: `Mission ${id}`, goal: `goal ${id}`, state, archived: false, rev: 1, budget_usd: 5, spent_usd: 1.2,
  counts: { done: 2, total: 7 }, ...extra,
})
const boards = {
  1: { rev: 1, counts: {}, tasks: { ready: [card(11, { session_id: 1, depends_on: [12] })], in_progress: [card(12, { session_id: 1, assignee: 'worker-1' })], review: [card(13, { session_id: 1, blocked: true, retries: 2 }), card(14, { session_id: 1, verified: true })] } },
  2: { rev: 1, counts: {}, tasks: { inbox: [card(21, { session_id: 2 })] } },
}
let listed
const choose = async (name) => fireEvent.click(await screen.findByRole('tab', { name }))
const mount = (ctx) => render(ctx ? <CoderBubbleContext.Provider value={ctx}><Tasks /></CoderBubbleContext.Provider> : <Tasks />)

beforeEach(() => {
  Object.values(api).forEach(f => f.mockReset())
  Object.values(sess).forEach(f => f.mockReset())
  ask.answer = true
  listed = [session(1, 'running'), session(2, 'draft')]
  api.board.mockResolvedValue({ rev: 1, counts: {}, tasks: { inbox: [card(1)], ready: [card(3)] } })
  api.agentShell.mockResolvedValue('')
  api.show.mockResolvedValue({ task: card(1), events: [] })
  api.move.mockResolvedValue({ id: 1 })
  sess.list.mockImplementation(async () => ({ sessions: listed }))
  sess.board.mockImplementation(async (id) => ({ ...boards[id], session: listed.find(s => s.id === id) }))
  sess.show.mockResolvedValue({ session: listed[0], events: [{ id: 1, actor: 'lead', kind: 'plan_approved', note: 'ok' }] })
  sess.plan.mockResolvedValue({ tasks: [
    { id: 11, title: 'Epic A', kind: 'epic', status: 'ready', acceptance: 'all children done' },
    { id: 12, title: 'Child one', parent_id: 11, status: 'in_progress', acceptance: 'tests pass', depends_on: [] },
    { id: 13, title: 'Child two', parent_id: 11, status: 'review', acceptance: 'docs written', depends_on: [12], blocked: true },
  ] })
  sess.pulse.mockResolvedValue({ session_id: 1, rev: 1, state: 'running' })
  for (const v of ['approve', 'pause', 'resume', 'stop', 'archive', 'budget']) sess[v].mockResolvedValue({ session: {} })
})
afterEach(cleanup)

describe('Tasks with sessions', () => {
  it('opens the session a chat card asked for (navData.sessionId)', async () => {
    render(<Tasks navData={{ sessionId: 2 }} />)
    expect(await screen.findByRole('region', { name: 'Session Mission 2' })).toBeInTheDocument()
  })
  it('keeps Personal as the board it was, with the sessions as tabs beside it', async () => {
    mount()
    expect(await screen.findByText('Task 1')).toBeInTheDocument()
    const tabs = await screen.findAllByRole('tab')
    expect(tabs.map(t => t.getAttribute('aria-selected'))).toEqual(['true', 'false', 'false'])
    expect(screen.queryByTestId('ts-approve')).not.toBeInTheDocument()
    expect(screen.getAllByLabelText('New task title')).toHaveLength(2)
    expect(sess.board).not.toHaveBeenCalled()
  })

  it('shows no switcher when there are no sessions', async () => {
    listed = []
    mount()
    expect(await screen.findByText('Task 1')).toBeInTheDocument()
    expect(screen.queryByRole('tablist')).not.toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('keeps Personal and says so when the session list cannot be read, then Retry brings the tabs', async () => {
    let fail = true
    sess.list.mockImplementation(async () => (fail ? { error: 'boom' } : { sessions: listed }))
    mount()
    expect(await screen.findByText('Task 1')).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: 'Personal' })).toBeInTheDocument()
    expect(screen.getByRole('alert')).toHaveTextContent('Could not read sessions')
    expect(screen.queryByRole('tab', { name: /Mission 1/ })).not.toBeInTheDocument()
    // Personal keeps working while the list is unreadable.
    expect(screen.getAllByLabelText('New task title').length).toBeGreaterThan(0)
    fail = false
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByRole('tab', { name: /Mission 1/ })).toBeInTheDocument()
    expect(screen.queryByText('Could not read sessions')).not.toBeInTheDocument()
    expect(screen.getByText('Task 1')).toBeInTheDocument()
  })

  it('shows state chip, progress and spend on every tab', async () => {
    mount()
    const tab = await screen.findByRole('tab', { name: /Mission 1/ })
    expect(within(tab).getByTestId('ts-state')).toHaveTextContent('Running')
    expect(within(tab).getByTestId('ts-progress')).toHaveTextContent('2/7')
    expect(within(tab).getByTestId('ts-spend')).toHaveTextContent('$1.20 of $5.00')
    expect(within(screen.getByRole('tab', { name: /Mission 2/ })).getByTestId('ts-state')).toHaveTextContent('Draft')
  })

  it('shows the session board with badges, the assignee and the dependency hint', async () => {
    mount()
    await choose(/Mission 1/)
    expect(await screen.findByText('Task 11')).toBeInTheDocument()
    expect(screen.queryByText('Task 1')).not.toBeInTheDocument()
    const ready = screen.getByRole('region', { name: 'Ready' })
    expect(within(ready).getByTestId('dep-hint')).toHaveTextContent('Waits for #12')
    expect(screen.getByTestId('assignee')).toHaveTextContent('worker-1')
    expect(screen.getByTestId('blocked')).toBeInTheDocument()
    expect(screen.getByTestId('verified')).toBeInTheDocument()
    expect(screen.getByTestId('retries')).toHaveTextContent('2')
    expect(screen.getAllByRole('region').map(r => r.getAttribute('aria-label')).slice(-5)).toEqual(['Inbox', 'Ready', 'In progress', 'Review', 'Done'])
    expect(screen.queryByLabelText('New task title')).not.toBeInTheDocument()
  })

  it('never shows another session\'s cards, even when its slow read answers late', async () => {
    let late
    sess.board.mockImplementation((id) => id === 1
      ? new Promise(r => { late = () => r({ ...boards[1], session: listed[0] }) })
      : Promise.resolve({ ...boards[2], session: listed[1] }))
    mount()
    await choose(/Mission 1/)
    await choose(/Mission 2/)
    expect(await screen.findByText('Task 21')).toBeInTheDocument()
    late()
    await new Promise(r => setTimeout(r, 20))
    expect(screen.queryByText('Task 11')).not.toBeInTheDocument()
    expect(screen.queryByText('Task 1')).not.toBeInTheDocument()
    await choose('Personal')
    expect(await screen.findByText('Task 1')).toBeInTheDocument()
    expect(screen.queryByText('Task 21')).not.toBeInTheDocument()
  })

  describe('header actions by state', () => {
    const cases = [
      ['draft', ['approve', 'stop', 'archive']],
      ['running', ['pause', 'stop']],
      ['paused', ['resume', 'stop']],
      ['done', ['archive']],
      ['stopped', ['archive']],
      ['failed', ['archive']],
    ]
    for (const [state, want] of cases) {
      it(`renders a ${state} session with ${want.join(', ')}`, async () => {
        listed = [session(1, state)]
        mount()
        await choose(/Mission 1/)
        await screen.findByTestId('ts-head-progress')
        expect(screen.getAllByTestId('ts-state').at(-1)).toHaveTextContent({ draft: 'Draft', running: 'Running', paused: 'Paused', done: 'Done', stopped: 'Stopped', failed: 'Failed' }[state])
        for (const a of ['approve', 'pause', 'resume', 'stop', 'archive']) {
          if (want.includes(a)) expect(screen.getByTestId(`ts-${a}`)).toBeInTheDocument()
          else expect(screen.queryByTestId(`ts-${a}`)).not.toBeInTheDocument()
        }
        expect(screen.getByRole('button', { name: 'Edit budget' })).toBeInTheDocument()
      })
    }
  })

  it('approves a draft with the session id, and has no approve button for a running session', async () => {
    mount()
    await choose(/Mission 1/)
    await screen.findByTestId('ts-pause')
    expect(screen.queryByTestId('ts-approve')).not.toBeInTheDocument()
    await choose(/Mission 2/)
    fireEvent.click(await screen.findByTestId('ts-approve'))
    await waitFor(() => expect(sess.approve).toHaveBeenCalledWith(2))
    expect(sess.pause).not.toHaveBeenCalled()
  })

  it('pauses, and stops only after the confirmation', async () => {
    mount()
    await choose(/Mission 1/)
    fireEvent.click(await screen.findByTestId('ts-pause'))
    await waitFor(() => expect(sess.pause).toHaveBeenCalledWith(1))
    ask.answer = false
    fireEvent.click(screen.getByTestId('ts-stop'))
    await new Promise(r => setTimeout(r, 10))
    expect(sess.stop).not.toHaveBeenCalled()
    ask.answer = true
    fireEvent.click(screen.getByTestId('ts-stop'))
    await waitFor(() => expect(sess.stop).toHaveBeenCalledWith(1))
  })

  it('shows the CLI refusal of an action', async () => {
    sess.approve.mockResolvedValue({ error: 'only you approve', code: 'operator_only' })
    mount()
    await choose(/Mission 2/)
    fireEvent.click(await screen.findByTestId('ts-approve'))
    expect(await screen.findByText('Refused: only you approve')).toBeInTheDocument()
  })

  it('edits the budget', async () => {
    mount()
    await choose(/Mission 1/)
    fireEvent.click(await screen.findByRole('button', { name: 'Edit budget' }))
    fireEvent.change(screen.getByLabelText(/Budget/), { target: { value: '12.5' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(sess.budget).toHaveBeenCalledWith(1, 12.5))
  })

  it('offers Open conversation only when the session has one and the app can open it', async () => {
    const openDraft = vi.fn()
    listed = [session(1, 'running', { conversation_id: 'conv-9' }), session(2, 'running')]
    mount({ openDraft })
    await choose(/Mission 1/)
    fireEvent.click(await screen.findByRole('button', { name: 'Open conversation' }))
    expect(openDraft).toHaveBeenCalledWith({ conversationId: 'conv-9' })
    await choose(/Mission 2/)
    await screen.findByTestId('ts-pause')
    expect(screen.queryByRole('button', { name: 'Open conversation' })).not.toBeInTheDocument()
  })

  it('shows the plan as a tree with acceptance and dependencies', async () => {
    mount()
    await choose(/Mission 1/)
    fireEvent.click(await screen.findByRole('button', { name: 'Plan', pressed: false }))
    const plan = await screen.findByRole('list', { name: 'Plan' })
    expect(within(plan).getByText('Epic A')).toBeInTheDocument()
    expect(within(plan).getByText('tests pass')).toBeInTheDocument()
    expect(within(plan).getByText('after #12')).toBeInTheDocument()
    // a child sits under its epic
    const epic = within(plan).getByText('Epic A').closest('li')
    expect(within(epic).getByText('Child one')).toBeInTheDocument()
  })

  it('opens a task in the drawer with acceptance, dependencies, artifacts and the session events', async () => {
    api.show.mockResolvedValue({
      task: card(11, { session_id: 1, acceptance: 'unit tests green', depends_on: [12], assignee: 'worker-1', retries: 1 }),
      events: [], artifacts: [{ id: 5, task_id: 11, kind: 'commit', ref: 'abc123', note: 'the fix' }],
    })
    mount()
    await choose(/Mission 1/)
    fireEvent.click(await screen.findByText('Task 11'))
    const drawer = await screen.findByRole('dialog')
    expect(await within(drawer).findByText('unit tests green')).toBeInTheDocument()
    expect(within(drawer).getByText('#12')).toBeInTheDocument()
    expect(within(drawer).getByText('worker-1')).toBeInTheDocument()
    expect(await within(drawer).findByText('abc123')).toBeInTheDocument()
    expect(await within(drawer).findByTestId('session-events')).toHaveTextContent('plan_approved')
  })

  it('moves a card by key in a running session, but not in a draft', async () => {
    mount()
    await choose(/Mission 1/)
    fireEvent.keyDown(await screen.findByRole('button', { name: 'Task 11' }), { key: 'ArrowRight', shiftKey: true })
    await waitFor(() => expect(api.move).toHaveBeenCalledWith(11, 'in_progress', { where: '' }))
    api.move.mockClear()
    await choose(/Mission 2/)
    fireEvent.keyDown(await screen.findByRole('button', { name: 'Task 21' }), { key: 'ArrowRight', shiftKey: true })
    await new Promise(r => setTimeout(r, 20))
    expect(api.move).not.toHaveBeenCalled()
    expect(screen.queryByRole('button', { name: /Approve to Ready/ })).not.toBeInTheDocument()
  })

  it('reads the session board again when its revision moves', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      mount()
      await choose(/Mission 1/)
      await screen.findByText('Task 11')
      const reads = sess.board.mock.calls.length
      sess.pulse.mockResolvedValue({ session_id: 1, rev: 2, state: 'running' })
      await vi.advanceTimersByTimeAsync(2100)
      await waitFor(() => expect(sess.board.mock.calls.length).toBeGreaterThan(reads))
    } finally { vi.useRealTimers() }
  })
})
