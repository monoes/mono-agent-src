// @vitest-environment jsdom
// The whole mission flow of a coder chat, fixture events in order, services mocked:
// propose -> Start mission -> plan (draft) -> Approve -> strip running -> report (close proposed) -> Close mission -> the report link opens the artifact drawer in Tasks.
import React, { useEffect, useState } from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup, act, within } from '@testing-library/react'
import '../../../i18n.js'

const { api, chatAccess, sess, tasksApi, listeners } = vi.hoisted(() => ({
  listeners: { chat: new Set() },
  chatAccess: { create: vi.fn(), setAccess: vi.fn(), record: vi.fn() },
  sess: { list: vi.fn(), show: vi.fn(), plan: vi.fn(), board: vi.fn(), approve: vi.fn(), pause: vi.fn(), resume: vi.fn(), stop: vi.fn(), archive: vi.fn(), close: vi.fn(), budget: vi.fn(), pulse: vi.fn() },
  tasksApi: { board: vi.fn(), show: vi.fn(), add: vi.fn(), edit: vi.fn(), move: vi.fn(), approve: vi.fn(), archive: vi.fn(), comment: vi.fn(), agentShell: vi.fn() },
  api: {
    getChatTurns: vi.fn(), getChatEvents: vi.fn(), startChatTurn: vi.fn(), confirmMission: vi.fn(), stopChatTurn: vi.fn(), coderStatus: vi.fn(),
    coderWorkspaceList: vi.fn(), getAgentRuntimeModels: vi.fn(), hilList: vi.fn(), getProfiles: vi.fn(),
  },
}))
vi.mock('../../../services/api.js', () => ({
  api, notify: vi.fn(), onChatEvent: cb => { listeners.chat.add(cb); return () => listeners.chat.delete(cb) },
  onOrgEvent: () => () => {}, onOrgRunStatus: () => () => {}, newOrgEventsStreamId: () => 's',
}))
vi.mock('../../../services/taskSessions.js', () => ({ sessionsApi: sess }))
vi.mock('../../../services/tasks.js', () => ({ tasksApi, onTasksChanged: () => () => {} }))
vi.mock('../../../lib/chatAccessApi.js', () => ({ chatAccessApi: chatAccess }))
vi.mock('../../ConfirmDialog.jsx', () => ({ confirm: () => Promise.resolve(true) }))

import CoderBubbles from '../CoderBubbles.jsx'
import { useCoderBubbles } from '../useCoderBubbles.js'
import Tasks from '../../../pages/Tasks.jsx'
import { forgetConversationSessions } from './useMissionSession.js'

function App() {
  const store = useCoderBubbles()
  const [nav, setNav] = useState(null)
  useEffect(() => { store.openConversation({ id: 'c1', cwd: '/w/p', model: 'opus', access: 'full', verified: true }) }, []) // eslint-disable-line react-hooks/exhaustive-deps
  return (
    <>
      <CoderBubbles store={store} onNavigate={(page, data) => { if (page === 'tasks') setNav(data) }} />
      {nav && <Tasks isActive navData={nav} />}
    </>
  )
}

const ev = (turn, seq, type, payload) => ({ conversationId: 'c1', turnId: turn, seq, at: `2026-10-25T10:00:${String(seq).padStart(2, '0')}.000Z`, type, payload })
const emit = (e) => act(async () => { listeners.chat.forEach(cb => cb(e)) })
const doc = (state, counts = { done: 0, total: 4 }) => ({ id: 7, title: 'Add CSV export', goal: 'g', state, archived: false, rev: state === 'running' ? 2 : 1, budget_usd: 5, spent_usd: 0.5, counts, conversation_id: 'c1' })
const card = (id, extra = {}) => ({ id, title: `Task ${id}`, notes: '', position: id, claim: null, session_id: 7, ...extra })
const integ = card(34, { kind: 'check', title: 'Integration check' })

let current
beforeEach(() => {
  vi.clearAllMocks()
  listeners.chat.clear()
  forgetConversationSessions()
  Element.prototype.scrollTo = function scrollTo() {}
  localStorage.clear()
  current = null // the session does not exist until the plan is made
  api.getChatTurns.mockResolvedValue({ items: [{ id: 't1', prompt: 'build a CSV export', status: 'completed' }] })
  api.getChatEvents.mockResolvedValue({
    items: [ev('t1', 1, 'turn.started', { text: 'build a CSV export' }), ev('t1', 2, 'mission.propose', { title: 'Add CSV export', goal: 'Export orders', reason: 'It touches 12 files' }), ev('t1', 9, 'turn.finished', { status: 'completed' })],
    hasMore: false,
  })
  api.coderStatus.mockResolvedValue({ enabled: true, ready: true, workspaceRoot: '/w' })
  api.coderWorkspaceList.mockResolvedValue([])
  api.getAgentRuntimeModels.mockResolvedValue([])
  api.hilList.mockResolvedValue([])
  api.getProfiles.mockResolvedValue([])
  api.confirmMission.mockResolvedValue({ ok: true, status: 'active' })
  chatAccess.record.mockResolvedValue({ access: 'full', mode: 'coder' })
  sess.list.mockImplementation(async () => ({ sessions: current ? [current] : [] }))
  sess.show.mockImplementation(async () => (current ? { session: current, events: [] } : { error: 'not found' }))
  sess.pulse.mockImplementation(async () => (current ? { session_id: 7, rev: current.rev, state: current.state } : {}))
  sess.board.mockImplementation(async () => ({ rev: 2, session: current, tasks: current?.state === 'done'
    ? { done: [card(31), integ] } : { in_progress: [card(31, { assignee: 'worker-1', title: 'Write exporter' })], ready: [card(32)] } }))
  sess.close.mockImplementation(async () => { current = doc('done', { done: 4, total: 4 }); return { session: current } })
  sess.plan.mockResolvedValue({ tasks: [card(31), integ] })
  sess.approve.mockImplementation(async () => { current = doc('running', { done: 1, total: 4 }); return { session: current } })
  tasksApi.board.mockResolvedValue({ rev: 1, counts: {}, tasks: {} })
  tasksApi.agentShell.mockResolvedValue('')
  tasksApi.show.mockImplementation(async (id) => ({ task: id === 34 ? integ : card(id), events: [], artifacts: id === 34 ? [{ id: 5, task_id: 34, kind: 'report', ref: 'final-report', note: 'Shipped CSV export; 14 tests pass.' }] : [] }))
})
afterEach(cleanup)

describe('mission flow, end to end', () => {
  it('runs from the proposal to the report and opens the report artifact in Tasks', async () => {
    render(<App />)

    // 1. propose -> Start mission sends the confirmation with the mission flag
    fireEvent.click(await screen.findByRole('button', { name: 'Start mission' }))
    await waitFor(() => expect(api.confirmMission).toHaveBeenCalledTimes(1))
    const [conv, turn, msg] = api.confirmMission.mock.calls[0]
    expect(conv).toBe('c1')
    expect(msg).toContain('Add CSV export')
    await waitFor(() => expect(screen.getByTestId('mission-started')).toBeInTheDocument())

    // 2. plan: the card shows the session's own draft state
    current = doc('draft')
    await emit(ev(turn, 1, 'turn.started', { text: msg }))
    await emit(ev(turn, 2, 'mission.plan', { sessionId: 7, title: 'Add CSV export', goal: 'Export orders', taskCount: 4, estimatedBudgetUsd: 2.5, round: 1 }))
    const plan = await screen.findByTestId('mission-plan')
    await waitFor(() => expect(screen.getByTestId('mission-plan-state')).toHaveTextContent('Draft'))
    expect(plan).toHaveTextContent('4 tasks')

    // 3. Approve calls TaskSessionApprove; the strip shows running with progress and a worker
    fireEvent.click(screen.getByTestId('mission-approve'))
    await waitFor(() => expect(sess.approve).toHaveBeenCalledWith(7))
    const strip = await screen.findByTestId('mission-strip')
    await waitFor(() => expect(within(strip).getByTestId('ts-state')).toHaveTextContent('Running'))
    expect(screen.getByTestId('mission-strip-progress')).toHaveTextContent('1/4')
    expect(await screen.findByTestId('mission-working')).toHaveTextContent('worker-1')
    expect(screen.queryByTestId('mission-approve')).toBeNull()

    // 4. report: the lead only proposed closing, so the session is still running and the strip says so;
    // the operator's Close mission button on the report card closes it (TaskSessionClose), then both read Done
    current = doc('running', { done: 4, total: 4 })
    await emit(ev(turn, 3, 'mission.report', { sessionId: 7, summary: 'Shipped CSV export; 14 tests pass.', closeProposed: true }))
    await emit(ev(turn, 4, 'turn.finished', { status: 'completed' }))
    const report = await screen.findByTestId('mission-report')
    expect(report).toHaveTextContent('Shipped CSV export; 14 tests pass.')
    await waitFor(() => expect(within(screen.getByTestId('mission-strip')).getByTestId('ts-state')).toHaveTextContent('Ready to close'))
    fireEvent.click(await within(report).findByRole('button', { name: 'Close mission' }))
    await waitFor(() => expect(sess.close).toHaveBeenCalledWith(7))
    await waitFor(() => expect(within(screen.getByTestId('mission-strip')).getByTestId('ts-state')).toHaveTextContent('Done'))
    expect(await within(report).findByText('Closed')).toBeInTheDocument()

    // 5. the report link opens the session in Tasks and the drawer of the task with the final report
    fireEvent.click(screen.getByTestId('mission-open-report'))
    const drawer = await screen.findByRole('dialog', { name: 'Integration check' })
    expect(await within(drawer).findByTestId('artifacts')).toHaveTextContent('final-report')
    expect(drawer).toHaveTextContent('Shipped CSV export; 14 tests pass.')
    expect(await screen.findByRole('region', { name: 'Session Add CSV export' })).toBeInTheDocument()
  })
})
