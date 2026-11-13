// @vitest-environment jsdom
// The pursuit panel in the mission strip: the first-run notice (the CLI's words), a visible Pause, what is missing (the CLI's
// readiness items) and the two authority switches, each written only through its binding after the operator's confirmation.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import '../../../i18n.js'

const { sess, go, ask } = vi.hoisted(() => ({
  sess: { show: vi.fn(), pulse: vi.fn(), list: vi.fn(), board: vi.fn() },
  go: { PursuitHostStatus: vi.fn(), PursuitReadiness: vi.fn(), PursuitPause: vi.fn(), MissionChildrenState: vi.fn(), SetMissionChildren: vi.fn(), GetChatMissionToggle: vi.fn(), SetAgentApproval: vi.fn(), PursuitStart: vi.fn(), PursuitStartPreview: vi.fn() },
  ask: vi.fn(),
}))
vi.mock('../../../services/api.js', () => ({ api: { openURL: vi.fn() }, notify: vi.fn() }))
vi.mock('../../../services/taskSessions.js', () => ({ sessionsApi: sess }))
vi.mock('../../../wailsjs/go/main/App', () => go)
vi.mock('../../ConfirmDialog.jsx', () => ({ confirm: (...a) => ask(...a) }))
import MissionStrip from './MissionStrip.jsx'
import PursuitPanel, { Boundary } from './PursuitPanel.jsx'
import { forgetConversationSessions } from './useMissionSession.js'

const NOTICE = 'This mission will pursue its goal while you are away, but only after you accept it. Pause with `task session pursuit pause <session-id>`.'
const doc = state => ({ id: 7, title: 'Mission 7', goal: 'g', state, archived: false, rev: 1, budget_usd: 5, spent_usd: 1, counts: { done: 1, total: 2 }, conversation_id: 'c1' })
const j = o => Promise.resolve(JSON.stringify(o))
const strip = async (state = 'running') => {
  sess.list.mockResolvedValue({ sessions: [doc(state)] })
  sess.show.mockResolvedValue({ session: doc(state) })
  render(<MissionStrip conversationId="c1" />)
  await screen.findByTestId('mission-strip')
}
beforeEach(() => {
  vi.clearAllMocks(); forgetConversationSessions(); localStorage.clear()
  sess.pulse.mockResolvedValue({}); sess.board.mockResolvedValue({ tasks: {} })
  ask.mockResolvedValue(true)
  go.PursuitHostStatus.mockReturnValue(j({ notice: NOTICE, pauseCommand: 'task session pursuit pause', missing: 'judge-pin', reason: 'judge-pin missing: run `coder host pursuit judge set RUNTIME/MODEL`', pendingRestart: false }))
  go.PursuitReadiness.mockReturnValue(j({ ready: false, items: [{ id: 'judge_pin', status: 'missing', detail: 'no judge', fix: 'coder host pursuit judge set RUNTIME/MODEL' }, { id: 'goal', status: 'ok', detail: 'fine' }], pursuit: { started: false } }))
  go.MissionChildrenState.mockReturnValue(j({ missionChildrenEnabled: false }))
  go.GetChatMissionToggle.mockReturnValue(j({ settings: { agent_approval: 'off' } }))
  go.PursuitPause.mockReturnValue(j({ ok: true }))
  go.SetMissionChildren.mockReturnValue(j({ missionChildrenEnabled: true }))
  go.SetAgentApproval.mockReturnValue(j({ ok: true }))
})
afterEach(cleanup)

describe('pursuit panel', () => {
  it('shows the CLI notice and the idle reason on first run, and stays dismissed after Got it', async () => {
    await strip()
    expect(await screen.findByTestId('pursuit-notice')).toHaveTextContent(NOTICE)
    expect(screen.getByTestId('pursuit-status-idle')).toHaveTextContent('judge-pin missing')
    fireEvent.click(screen.getByTestId('pursuit-notice-dismiss'))
    expect(screen.queryByTestId('pursuit-notice')).toBeNull()
    expect(screen.getByTestId('pursuit-status-idle')).toBeInTheDocument() // only the explanation is dismissible
    cleanup(); forgetConversationSessions()
    await strip()
    await waitFor(() => expect(go.PursuitHostStatus).toHaveBeenCalledTimes(2))
    expect(screen.queryByTestId('pursuit-notice')).toBeNull()
    expect(await screen.findByTestId('pursuit-status-idle')).toBeInTheDocument()
  })
  it('a dismissal is per mission, not global, and never hides a pending restart', async () => {
    localStorage.setItem('monoagent-pursuit-notice-seen:99', '1') // another mission's dismissal
    go.PursuitHostStatus.mockReturnValue(j({ notice: NOTICE, pendingRestart: true }))
    await strip()
    expect(await screen.findByTestId('pursuit-notice')).toBeInTheDocument()
    fireEvent.click(screen.getByTestId('pursuit-notice-dismiss'))
    expect(screen.getByTestId('pursuit-status-restart')).toBeInTheDocument()
  })
  it('says plainly when the saved switch is off, with the fix, even after the notice was dismissed', async () => {
    localStorage.setItem('monoagent-pursuit-notice-seen:7', '1')
    go.PursuitHostStatus.mockReturnValue(j({ notice: NOTICE, savedOperatorOff: true }))
    await strip()
    expect(await screen.findByTestId('pursuit-status-off-saved')).toHaveTextContent('coder host pursuit on')
  })
  it('says an environment OFF must be unset, since the CLI cannot override it', async () => {
    go.PursuitHostStatus.mockReturnValue(j({ savedOperatorOff: false, operatorOff: true, host: { found: true, alive: true, pursuit: { offSource: 'env' } } }))
    await strip()
    const l = await screen.findByTestId('pursuit-status-off-env')
    expect(l).toHaveTextContent('MONOAGENT_PURSUIT_LOOP')
    expect(l).toHaveTextContent('unset')
  })
  it('an unreadable saved setting is "could not read", never "off"', async () => {
    go.PursuitHostStatus.mockReturnValue(j({ savedOperatorOff: null, readError: 'database is locked' }))
    await strip()
    expect(await screen.findByTestId('pursuit-status-read-error')).toHaveTextContent('Could not read')
    expect(screen.queryByTestId('pursuit-status-off-saved')).toBeNull()
  })
  it('says a restart is pending when the CLI does', async () => {
    go.PursuitHostStatus.mockReturnValue(j({ notice: NOTICE, pendingRestart: true }))
    await strip()
    expect(await screen.findByTestId('pursuit-status-restart')).toBeInTheDocument()
  })
  it('has a visible Pause button for a running mission; pressing it runs the pause binding for that session', async () => {
    go.PursuitReadiness.mockReturnValue(j({ items: [], pursuit: { started: true } }))
    await strip()
    const pause = await screen.findByTestId('pursuit-pause')
    await waitFor(() => expect(pause).toBeEnabled())
    fireEvent.click(pause)
    await waitFor(() => expect(go.PursuitPause).toHaveBeenCalledWith(7))
  })
  it('shows the CLI refusal of a pause and disables Pause while the CLI says no pursuit has started', async () => {
    await strip()
    await waitFor(() => expect(screen.getByTestId('pursuit-pause')).toBeDisabled())
    go.PursuitReadiness.mockReturnValue(j({ items: [], pursuit: { started: true } }))
    go.PursuitPause.mockReturnValue(j({ error: 'only the user can pause' }))
    cleanup(); forgetConversationSessions()
    await strip()
    await waitFor(() => expect(screen.getByTestId('pursuit-pause')).toBeEnabled())
    fireEvent.click(screen.getByTestId('pursuit-pause'))
    expect(await screen.findByTestId('pursuit-note')).toHaveTextContent('only the user can pause')
  })
  it('lists only what is missing, with the CLI fix', async () => {
    await strip()
    const li = await screen.findByTestId('pursuit-missing-judge_pin')
    expect(li).toHaveTextContent('coder host pursuit judge set RUNTIME/MODEL')
    expect(screen.queryByTestId('pursuit-missing-goal')).toBeNull()
  })
  it('turns the child-sessions switch on only after the operator confirms, and not when declined', async () => {
    await strip()
    const box = await screen.findByTestId('pursuit-children')
    await waitFor(() => expect(box).toBeEnabled())
    ask.mockResolvedValueOnce(false)
    fireEvent.click(box)
    await waitFor(() => expect(ask).toHaveBeenCalledTimes(1))
    expect(go.SetMissionChildren).not.toHaveBeenCalled()
    fireEvent.click(box)
    await waitFor(() => expect(go.SetMissionChildren).toHaveBeenCalledWith(true))
  })
  it('lead approval needs a ceiling above 0, then a confirmation, then writes agent_approval through the binding', async () => {
    await strip()
    const box = await screen.findByTestId('pursuit-approval')
    await waitFor(() => expect(box).toBeEnabled())
    fireEvent.click(box)
    expect(await screen.findByTestId('pursuit-note')).toBeInTheDocument()
    expect(ask).not.toHaveBeenCalled()
    expect(go.SetAgentApproval).not.toHaveBeenCalled()
    fireEvent.change(screen.getByTestId('pursuit-ceiling'), { target: { value: '4' } })
    fireEvent.click(box)
    await waitFor(() => expect(go.SetAgentApproval).toHaveBeenCalledWith('c1', true, 4))
  })
  it.each([
    ['items as an object', { items: { a: 1 }, pursuit: { started: true } }],
    ['items as a string', { items: 'nope', pursuit: 'x' }],
    ['items with junk entries', { items: [null, 5, 'x', { status: 'missing' }, { id: 'ok', status: 'missing', detail: { a: 1 }, fix: 7 }] }],
    ['a null document', null],
  ])('survives a malformed readiness: %s', async (_n, doc) => {
    go.PursuitReadiness.mockReturnValue(j(doc))
    await strip()
    expect(await screen.findByTestId('pursuit-pause')).toBeInTheDocument()
    expect(screen.getByTestId('mission-strip')).toBeInTheDocument()
  })
  it.each([
    ['notice as an object', { notice: { x: 1 }, missing: ['a'], pendingRestart: 'yes', host: 5 }],
    ['an array', [1, 2]],
    ['fields of the wrong type', { notice: 5, reason: {}, missing: 7, savedOperatorOff: 'true', operatorOff: true, host: { pursuit: [] } }],
  ])('survives a malformed status: %s', async (_n, doc) => {
    go.PursuitHostStatus.mockReturnValue(j(doc))
    go.MissionChildrenState.mockReturnValue(j({ missionChildrenEnabled: 'yes' }))
    go.GetChatMissionToggle.mockReturnValue(j({ settings: 3 }))
    await strip()
    expect(await screen.findByTestId('pursuit-pause')).toBeInTheDocument()
    expect(screen.queryByTestId('pursuit-notice')).toBeNull()
    expect(screen.getByTestId('pursuit-children')).toBeDisabled()
  })
  it('the error boundary contains a crash and shows the fallback', () => {
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {})
    const Bomb = () => { throw new Error('boom') }
    render(<div data-testid="outside"><Boundary fallback="fallback text"><Bomb /></Boundary></div>)
    expect(screen.getByTestId('pursuit-panel-broken')).toHaveTextContent('fallback text')
    expect(screen.getByTestId('outside')).toBeInTheDocument()
    spy.mockRestore()
  })
  it('ignores the answer of an older request after the chat changed', async () => {
    let release
    go.PursuitHostStatus.mockImplementationOnce(() => new Promise(r => { release = r }))
    const { rerender } = render(<PursuitPanel sessionId={7} conversationId="c1" />)
    go.PursuitHostStatus.mockReturnValue(j({ notice: 'SECOND', missing: '' }))
    rerender(<PursuitPanel sessionId={7} conversationId="c2" />)
    expect(await screen.findByTestId('pursuit-notice')).toHaveTextContent('SECOND')
    release(JSON.stringify({ notice: 'STALE', missing: '' }))
    await new Promise(r => setTimeout(r, 20))
    expect(screen.queryByText('STALE')).toBeNull()
    expect(screen.getByTestId('pursuit-notice')).toHaveTextContent('SECOND')
  })
  it('a dismissal made on one mission does not hide the notice of the next one', async () => {
    const { rerender } = render(<PursuitPanel sessionId={7} conversationId="c1" />)
    fireEvent.click(await screen.findByTestId('pursuit-notice-dismiss'))
    expect(screen.queryByTestId('pursuit-notice')).toBeNull()
    rerender(<PursuitPanel sessionId={8} conversationId="c1" />)
    expect(await screen.findByTestId('pursuit-notice')).toBeInTheDocument()
  })
  it('Pause is disabled while in flight, and says Paused when it went through', async () => {
    go.PursuitReadiness.mockReturnValue(j({ items: [], pursuit: { started: true } }))
    let release
    go.PursuitPause.mockImplementation(() => new Promise(r => { release = r }))
    await strip()
    const pause = await screen.findByTestId('pursuit-pause')
    await waitFor(() => expect(pause).toBeEnabled())
    fireEvent.click(pause)
    await waitFor(() => expect(pause).toBeDisabled())
    fireEvent.click(pause)
    expect(go.PursuitPause).toHaveBeenCalledTimes(1)
    release(JSON.stringify({ ok: true }))
    expect(await screen.findByTestId('pursuit-done')).toHaveTextContent('Paused')
  })
  it('does not render for a finished mission', async () => {
    sess.list.mockResolvedValue({ sessions: [doc('done')] })
    sess.show.mockResolvedValue({ session: doc('done') })
    render(<MissionStrip conversationId="c1" />)
    await new Promise(r => setTimeout(r, 20))
    expect(screen.queryByTestId('pursuit-panel')).toBeNull()
  })
  it('stays calm when the CLI is not available: no notice, Pause still there', async () => {
    for (const k of ['PursuitHostStatus', 'PursuitReadiness', 'MissionChildrenState', 'GetChatMissionToggle']) go[k].mockReturnValue(j({ error: 'unavailable' }))
    await strip()
    expect(await screen.findByTestId('pursuit-pause')).toBeInTheDocument()
    expect(screen.queryByTestId('pursuit-notice')).toBeNull()
  })
})
