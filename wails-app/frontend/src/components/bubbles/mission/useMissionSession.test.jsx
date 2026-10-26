// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook, act, waitFor, cleanup } from '@testing-library/react'

const { api } = vi.hoisted(() => ({ api: { show: vi.fn(), board: vi.fn(), pulse: vi.fn(), list: vi.fn() } }))
vi.mock('../../../services/taskSessions.js', () => ({ sessionsApi: api }))
import { useSessionLive } from './useMissionSession.js'

const deferred = () => { let resolve; const promise = new Promise(r => { resolve = r }); return { promise, resolve } }
const doc = (id) => ({ session: { id, title: `S${id}`, state: 'running', rev: 1 } })
beforeEach(() => { Object.values(api).forEach(f => f.mockReset()); api.pulse.mockResolvedValue({}) })
afterEach(cleanup)

describe('useSessionLive', () => {
  it('a late read of the old session after switching never shows, and its board never lands', async () => {
    const a = deferred()
    api.show.mockImplementation((id) => (id === 1 ? a.promise : Promise.resolve(doc(2))))
    api.board.mockImplementation(async (id) => ({ tasks: { in_progress: [{ id: 10 * id, title: `task of ${id}` }] } }))
    const { result, rerender } = renderHook(({ id }) => useSessionLive(id, { withBoard: true }), { initialProps: { id: 1 } })
    rerender({ id: 2 })
    await waitFor(() => expect(result.current.session?.id).toBe(2))
    await waitFor(() => expect(result.current.board?.tasks.in_progress[0].title).toBe('task of 2'))
    await act(async () => { a.resolve(doc(1)) })
    expect(result.current.session.id).toBe(2)
    expect(result.current.board.tasks.in_progress[0].title).toBe('task of 2')
  })

  it('resets session and board when the id changes', async () => {
    const b = deferred()
    api.show.mockImplementation((id) => (id === 1 ? Promise.resolve(doc(1)) : b.promise))
    api.board.mockResolvedValue({ tasks: { in_progress: [{ id: 1, title: 'one' }] } })
    const { result, rerender } = renderHook(({ id }) => useSessionLive(id, { withBoard: true }), { initialProps: { id: 1 } })
    await waitFor(() => expect(result.current.board).toBeTruthy())
    rerender({ id: 2 })
    expect(result.current.session).toBeNull()
    expect(result.current.board).toBeNull()
  })
})
