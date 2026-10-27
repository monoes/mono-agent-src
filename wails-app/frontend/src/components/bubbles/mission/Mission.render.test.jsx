// @vitest-environment jsdom
// The mission cards, strip and templates in an expanded coder bubble.
import React, { useEffect } from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
import '../../../i18n.js'

const { api, chatAccess, sess, nav } = vi.hoisted(() => ({
  nav: vi.fn(),
  chatAccess: { create: vi.fn(), setAccess: vi.fn(), record: vi.fn() },
  sess: { list: vi.fn(), show: vi.fn(), plan: vi.fn(), board: vi.fn(), approve: vi.fn(), pause: vi.fn(), resume: vi.fn(), stop: vi.fn(), archive: vi.fn(), close: vi.fn(), budget: vi.fn(), pulse: vi.fn() },
  api: {
    getChatTurns: vi.fn(), getChatEvents: vi.fn(), startChatTurn: vi.fn(), confirmMission: vi.fn(), stopChatTurn: vi.fn(), coderStatus: vi.fn(),
    coderWorkspaceList: vi.fn(), getAgentRuntimeModels: vi.fn(), hilList: vi.fn(), getProfiles: vi.fn(),
  },
}))
vi.mock('../../../services/api.js', () => ({ api, notify: vi.fn(), onChatEvent: () => () => {}, onOrgEvent: () => () => {}, onOrgRunStatus: () => () => {}, newOrgEventsStreamId: () => 's' }))
vi.mock('../../../services/taskSessions.js', () => ({ sessionsApi: sess }))
vi.mock('../../../lib/chatAccessApi.js', () => ({ chatAccessApi: chatAccess }))
const ask = vi.hoisted(() => ({ ok: true }))
vi.mock('../../ConfirmDialog.jsx', () => ({ confirm: () => Promise.resolve(ask.ok) }))

import CoderBubbles from '../CoderBubbles.jsx'
import { useCoderBubbles } from '../useCoderBubbles.js'
import { forgetConversationSessions } from './useMissionSession.js'

function Harness({ open }) {
  const store = useCoderBubbles()
  useEffect(() => { store.openConversation(open) }, []) // eslint-disable-line react-hooks/exhaustive-deps
  return <CoderBubbles store={store} onNavigate={nav} />
}
let n = 0
const conv = () => ({ id: 'c1', cwd: '/w/p', model: 'opus', access: 'full', verified: true })
const mev = (turn, seq, type, payload) => ({ conversationId: 'c1', turnId: turn, seq, at: '2026-10-25T10:00:0' + seq + '.000Z', type, payload })
const turnsWith = (...turns) => {
  api.getChatTurns.mockResolvedValue({ items: turns.map(t => ({ id: t.id, prompt: 'build it', status: 'completed' })).reverse() })
  api.getChatEvents.mockImplementation(async (c, turnId) => ({
    items: [mev(turnId, 1, 'turn.started', { text: 'build it' }), ...(turns.find(t => t.id === turnId)?.events || []), mev(turnId, 9, 'turn.finished', { status: 'completed' })], hasMore: false,
  }))
}
const sessionDoc = (id, state, extra = {}) => ({ id, title: `Mission ${id}`, goal: 'g', state, archived: false, rev: 1, budget_usd: 5, spent_usd: 1.5, counts: { done: 2, total: 7 }, conversation_id: 'c1', ...extra })

beforeEach(() => {
  vi.clearAllMocks()
  forgetConversationSessions()
  ask.ok = true
  n++
  Element.prototype.scrollTo = function scrollTo() {}
  localStorage.clear()
  api.getChatEvents.mockResolvedValue({ items: [], hasMore: false })
  api.getChatTurns.mockResolvedValue({ items: [] })
  api.coderStatus.mockResolvedValue({ enabled: true, ready: true, workspaceRoot: '/w' })
  api.coderWorkspaceList.mockResolvedValue([])
  api.getAgentRuntimeModels.mockResolvedValue([])
  api.hilList.mockResolvedValue([])
  api.getProfiles.mockResolvedValue([])
  api.startChatTurn.mockResolvedValue({ ok: true })
  api.confirmMission.mockResolvedValue({ ok: true, status: 'active' })
  chatAccess.record.mockResolvedValue({ access: 'full', mode: 'coder' })
  sess.list.mockResolvedValue({ sessions: [] })
  sess.show.mockResolvedValue({ error: 'not found' })
  sess.board.mockResolvedValue({ tasks: {} })
  sess.pulse.mockResolvedValue({})
  for (const v of ['approve', 'pause', 'resume', 'stop', 'archive']) sess[v].mockResolvedValue({ session: {} })
})
afterEach(cleanup)

describe('proposal card', () => {
  const propose = { title: 'Add CSV export', goal: 'Export orders as CSV', reason: 'It touches 12 files' }
  it('shows title, goal and reason; Start mission sends the confirmation with the mission flag', async () => {
    turnsWith({ id: 't1', events: [mev('t1', 2, 'mission.propose', propose)] })
    render(<Harness open={conv()} />)
    const card = await screen.findByTestId('mission-propose')
    expect(card).toHaveTextContent('Add CSV export')
    expect(card).toHaveTextContent('Export orders as CSV')
    expect(card).toHaveTextContent('It touches 12 files')
    fireEvent.click(screen.getByRole('button', { name: 'Start mission' }))
    await waitFor(() => expect(api.confirmMission).toHaveBeenCalledTimes(1))
    expect(api.confirmMission).toHaveBeenCalledWith('c1', expect.any(String), expect.stringContaining('Add CSV export'))
    expect(api.startChatTurn).not.toHaveBeenCalled()
    await waitFor(() => expect(screen.getByTestId('mission-started')).toBeInTheDocument())
  })
  it('with two proposals, the one followed by a plan is started and the later pending one still offers Start', async () => {
    turnsWith(
      { id: 't1', events: [mev('t1', 2, 'mission.propose', { title: 'First mission', goal: 'g1' }), mev('t1', 3, 'mission.plan', { sessionId: 7, title: 'First mission', taskCount: 1, round: 0 })] },
      { id: 't2', events: [mev('t2', 2, 'mission.propose', { title: 'Second mission', goal: 'g2' })] },
    )
    sess.show.mockResolvedValue({ session: sessionDoc(7, 'done') })
    render(<Harness open={conv()} />)
    const cards = await screen.findAllByTestId('mission-propose')
    expect(cards).toHaveLength(2)
    await waitFor(() => expect(screen.getAllByTestId('mission-started')).toHaveLength(1))
    expect(cards[0]).toHaveTextContent('Mission started.')
    expect(cards[1]).not.toHaveTextContent('Mission started.')
    expect(screen.getAllByRole('button', { name: 'Start mission' })).toHaveLength(1)
  })
  it('Not now is remembered and sends nothing', async () => {
    turnsWith({ id: 't1', events: [mev('t1', 2, 'mission.propose', propose)] })
    render(<Harness open={conv()} />)
    await screen.findByTestId('mission-propose')
    fireEvent.click(screen.getByRole('button', { name: 'Not now' }))
    expect(screen.getByTestId('mission-declined')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Start mission' })).toBeNull()
    expect(api.confirmMission).not.toHaveBeenCalled()
  })
})

describe('plan card', () => {
  const plan = (round) => ({ sessionId: 7, title: 'Add CSV export', goal: 'Export orders', taskCount: 6, estimatedBudgetUsd: 2.5, round })
  it('reads the real state: Approve while draft, calls TaskSessionApprove, and disappears once running', async () => {
    turnsWith({ id: 't1', events: [mev('t1', 2, 'mission.plan', plan(1))] })
    let state = 'draft'
    sess.show.mockImplementation(async () => ({ session: sessionDoc(7, state, { conversation_id: 'c1' }) }))
    sess.approve.mockImplementation(async () => { state = 'running'; return { session: {} } })
    render(<Harness open={conv()} />)
    const card = await screen.findByTestId('mission-plan')
    // live values from the stored session (7 tasks, $5 budget), not the event's (6, $2.50)
    expect(card).toHaveTextContent('Mission 7')
    expect(card).not.toHaveTextContent('Add CSV export')
    expect(card).toHaveTextContent('7 tasks')
    expect(card).toHaveTextContent('about $5.00')
    expect(card).toHaveTextContent('review round 1')
    const approve = await screen.findByTestId('mission-approve')
    fireEvent.click(approve)
    await waitFor(() => expect(sess.approve).toHaveBeenCalledWith(7))
    await waitFor(() => expect(screen.queryByTestId('mission-approve')).toBeNull())
    expect(screen.getByTestId('mission-plan-state')).toHaveTextContent('Running')
    expect(screen.queryByTestId('mission-reject')).toBeNull()
  })
  it('a forged sessionId pointing at another conversation shows no actions and no foreign title', async () => {
    turnsWith({ id: 't1', events: [mev('t1', 2, 'mission.plan', { sessionId: 17, title: 'Add docs', estimatedBudgetUsd: 1, taskCount: 1, round: 0 })] })
    sess.show.mockResolvedValue({ session: sessionDoc(17, 'draft', { conversation_id: 'c2', title: 'Rewrite billing', budget_usd: 500 }) })
    render(<Harness open={conv()} />)
    await screen.findByTestId('mission-plan-unavailable')
    expect(screen.getByTestId('mission-plan')).toHaveTextContent('Plan not available.')
    expect(screen.queryByTestId('mission-approve')).toBeNull()
    expect(screen.queryByTestId('mission-reject')).toBeNull()
    expect(screen.queryByTestId('mission-edit')).toBeNull()
    expect(screen.queryByText(/Rewrite billing|Add docs|\$500/)).toBeNull()
  })
  it('a missing session shows the neutral line and no actions', async () => {
    turnsWith({ id: 't1', events: [mev('t1', 2, 'mission.plan', plan(0))] })
    sess.show.mockResolvedValue({ error: 'not found' })
    render(<Harness open={conv()} />)
    await screen.findByTestId('mission-plan-unavailable')
    expect(screen.queryByTestId('mission-approve')).toBeNull()
  })
  it('does not trust the event: a plan already running offers no Approve', async () => {
    turnsWith({ id: 't1', events: [mev('t1', 2, 'mission.plan', plan(0))] })
    sess.show.mockResolvedValue({ session: sessionDoc(7, 'running') })
    render(<Harness open={conv()} />)
    await waitFor(() => expect(screen.getByTestId('mission-plan-state')).toHaveTextContent('Running'))
    expect(screen.queryByTestId('mission-approve')).toBeNull()
  })
  it('Edit in Tasks navigates with the session; Reject stops it after confirming', async () => {
    turnsWith({ id: 't1', events: [mev('t1', 2, 'mission.plan', plan(0))] })
    sess.show.mockResolvedValue({ session: sessionDoc(7, 'draft') })
    render(<Harness open={conv()} />)
    fireEvent.click(await screen.findByTestId('mission-edit'))
    expect(nav).toHaveBeenCalledWith('tasks', { sessionId: 7 })
    cleanup()
    render(<Harness open={conv()} />)
    fireEvent.click(await screen.findByTestId('mission-reject'))
    await waitFor(() => expect(sess.stop).toHaveBeenCalledWith(7))
  })
  it('shows one card per session: the latest plan replaces an earlier one', async () => {
    turnsWith({ id: 't1', events: [mev('t1', 2, 'mission.plan', plan(0))] }, { id: 't2', events: [mev('t2', 2, 'mission.plan', plan(2))] })
    sess.show.mockResolvedValue({ session: sessionDoc(7, 'draft') })
    render(<Harness open={conv()} />)
    await screen.findByTestId('mission-plan')
    expect(screen.getAllByTestId('mission-plan')).toHaveLength(1)
    expect(screen.getByTestId('mission-plan')).toHaveTextContent('review round 2')
  })
})

describe('mission strip', () => {
  it('shows this conversation\'s running session with progress, spend and workers, never another chat\'s', async () => {
    sess.list.mockResolvedValue({ sessions: [sessionDoc(8, 'running', { conversation_id: 'c2', title: 'Other chat mission' }), sessionDoc(7, 'running')] })
    sess.show.mockImplementation(async (id) => ({ session: sessionDoc(id, 'running') }))
    sess.board.mockResolvedValue({ tasks: { in_progress: [{ id: 1, title: 'Write exporter', assignee: 'worker-1' }] } })
    render(<Harness open={conv()} />)
    const strip = await screen.findByTestId('mission-strip')
    expect(strip).toHaveTextContent('Mission 7')
    expect(screen.getByTestId('mission-strip-progress')).toHaveTextContent('2/7')
    expect(screen.getByTestId('mission-strip-spend')).toHaveTextContent('$1.50 of $5.00')
    await waitFor(() => expect(screen.getByTestId('mission-working')).toHaveTextContent('Write exporter'))
    expect(screen.getByTestId('mission-working')).toHaveTextContent('worker-1')
    expect(screen.queryByText('Other chat mission')).toBeNull()
    fireEvent.click(screen.getByTestId('mission-pause'))
    await waitFor(() => expect(sess.pause).toHaveBeenCalledWith(7))
  })
  it('stops with a confirmation', async () => {
    sess.list.mockResolvedValue({ sessions: [sessionDoc(7, 'running')] })
    sess.show.mockResolvedValue({ session: sessionDoc(7, 'running') })
    render(<Harness open={conv()} />)
    fireEvent.click(await screen.findByTestId('mission-stop'))
    await waitFor(() => expect(sess.stop).toHaveBeenCalledWith(7))
  })
  it('does not show for a finished or someone else\'s session', async () => {
    sess.list.mockResolvedValue({ sessions: [sessionDoc(7, 'done'), sessionDoc(8, 'running', { conversation_id: 'c2' })] })
    sess.show.mockResolvedValue({ session: sessionDoc(7, 'done') })
    render(<Harness open={conv()} />)
    await waitFor(() => expect(sess.list).toHaveBeenCalled())
    await waitFor(() => expect(sess.show).toHaveBeenCalled())
    expect(screen.queryByTestId('mission-strip')).toBeNull()
  })
})

describe('report card', () => {
  it('shows the summary with a link to the session, and the strip reads Done', async () => {
    turnsWith({ id: 't1', events: [mev('t1', 2, 'mission.report', { sessionId: 7, summary: 'Shipped CSV export; 14 tests pass.' })] })
    sess.list.mockResolvedValue({ sessions: [sessionDoc(7, 'done')] })
    sess.show.mockResolvedValue({ session: sessionDoc(7, 'done') })
    render(<Harness open={conv()} />)
    const card = await screen.findByTestId('mission-report')
    expect(card).toHaveTextContent('Shipped CSV export; 14 tests pass.')
    const strip = await screen.findByTestId('mission-strip')
    expect(strip).toHaveTextContent('Done')
    fireEvent.click(screen.getByTestId('mission-open-report'))
    expect(nav).toHaveBeenCalledWith('tasks', { sessionId: 7, report: true })
  })
})

describe('mission templates', () => {
  it('inserts a skeleton into the composer', async () => {
    render(<Harness open={conv()} />)
    fireEvent.click(await screen.findByTestId('mission-templates-btn'))
    fireEvent.click(screen.getByRole('menuitem', { name: 'Bug fix' }))
    const box = document.querySelector('textarea')
    expect(box.value).toContain('Goal:')
    expect(box.value).toContain('Reproduction:')
    expect(box.value).toContain('Non-goals:')
    expect(n).toBeGreaterThan(0)
  })
  it('closes only the menu on Escape (with focus on its button), not the whole chat', async () => {
    render(<Harness open={conv()} />)
    const btn = await screen.findByTestId('mission-templates-btn')
    btn.focus()
    fireEvent.click(btn)
    expect(screen.getByRole('menu')).toBeInTheDocument()
    fireEvent.keyDown(btn, { key: 'Escape' })
    expect(screen.queryByRole('menu')).not.toBeInTheDocument()
    expect(screen.getByTestId('mission-templates-btn')).toBeInTheDocument()
  })
})
