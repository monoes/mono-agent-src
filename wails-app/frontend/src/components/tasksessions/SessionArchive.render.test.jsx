// @vitest-environment jsdom
// Archive on a session header: open sessions below it make the archive a cascade, after a confirm that
// says how many and which; the CLI's refusal shows inline; the hook sends the cascade verb to the binding.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { act, cleanup, fireEvent, render, renderHook, screen, waitFor } from '@testing-library/react'
import '../../i18n.js'

const ask = vi.hoisted(() => ({ ok: true, text: [] }))
const api = vi.hoisted(() => ({ list: vi.fn(), show: vi.fn(), archive: vi.fn(), archiveCascade: vi.fn() }))
vi.mock('../ConfirmDialog.jsx', () => ({ confirm: (m) => { ask.text.push(m); return Promise.resolve(ask.ok) } }))
vi.mock('../../services/taskSessions.js', () => ({ sessionsApi: api }))
import SessionHeader from './SessionHeader.jsx'
import { useTaskSessions } from '../../lib/useTaskSessions.js'

const sess = (id, state, extra = {}) => ({ id, title: `Session ${id}`, goal: '', state, archived: false, rev: 1, budget_usd: 0, spent_usd: 0, counts: { done: 0, total: 1 }, ...extra })
const root = sess(1, 'stopped')
const head = (sessions, props = {}) => render(<SessionHeader sessions={sessions} session={root} view="board" onView={() => {}} onAct={props.onAct || vi.fn()} onBudget={() => {}} notice={props.notice} onClearNotice={() => {}} />)
const archive = () => fireEvent.click(screen.getByTestId('ts-archive'))

beforeEach(() => { ask.ok = true; ask.text = []; Object.values(api).forEach(f => f.mockReset()) })
afterEach(cleanup)

describe('Archive with sessions below', () => {
  it('archives at once, with no question, when nothing below is open', async () => {
    const onAct = vi.fn().mockResolvedValue({})
    head([root, sess(2, 'done', { parent_session_id: 1 })], { onAct })
    archive()
    await waitFor(() => expect(onAct).toHaveBeenCalledWith('archive', 1))
    expect(ask.text).toEqual([])
  })
  it('asks first, naming how many and which, then cascades', async () => {
    const onAct = vi.fn().mockResolvedValue({})
    head([root, sess(2, 'draft', { parent_session_id: 1 }), sess(3, 'running', { parent_session_id: 2 }), sess(4, 'draft')], { onAct })
    archive()
    await waitFor(() => expect(onAct).toHaveBeenCalledWith('archiveCascade', 1))
    expect(ask.text).toHaveLength(1)
    expect(ask.text[0]).toContain('2 sessions below it')
    expect(ask.text[0]).toContain('Session 2; Session 3')
    expect(ask.text[0]).not.toContain('Session 4')
  })
  it('says "1 session" for one', async () => {
    head([root, sess(2, 'paused', { parent_session_id: 1 })])
    archive()
    await waitFor(() => expect(ask.text).toHaveLength(1))
    expect(ask.text[0]).toContain('and 1 session below it')
  })
  it('does not turn another action into a cascade', async () => {
    const onAct = vi.fn().mockResolvedValue({})
    render(<SessionHeader sessions={[sess(1, 'running'), sess(2, 'running', { parent_session_id: 1 })]} session={sess(1, 'running')} view="board" onView={() => {}} onAct={onAct} onBudget={() => {}} onClearNotice={() => {}} />)
    fireEvent.click(screen.getByTestId('ts-pause'))
    await waitFor(() => expect(onAct).toHaveBeenCalledWith('pause', 1))
    expect(ask.text).toEqual([])
  })
  it('does nothing when the operator declines', async () => {
    ask.ok = false
    const onAct = vi.fn()
    head([root, sess(2, 'running', { parent_session_id: 1 })], { onAct })
    archive()
    await waitFor(() => expect(ask.text).toHaveLength(1))
    expect(onAct).not.toHaveBeenCalled()
  })
  it('shows the CLI refusal inline', () => {
    head([root], { notice: { error: 'session #1 has 1 open sessions below it: #2 "x" (draft)', code: 'not_ready' } })
    expect(screen.getByRole('alert')).toHaveTextContent('has 1 open sessions below it')
  })
  it('sends the cascade verb to the binding and keeps its refusal in notice', async () => {
    api.list.mockResolvedValue({ sessions: [root] })
    api.archiveCascade.mockResolvedValue({ error: 'only the operator may archive', code: 'operator_only' })
    const { result } = renderHook(() => useTaskSessions(true, null))
    await act(async () => { await result.current.act('archiveCascade', 1) })
    expect(api.archiveCascade).toHaveBeenCalledWith(1)
    expect(api.archive).not.toHaveBeenCalled()
    expect(result.current.notice).toEqual({ error: 'only the operator may archive', code: 'operator_only' })
  })
})
