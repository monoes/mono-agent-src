// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook, act, waitFor, cleanup } from '@testing-library/react'

const { api } = vi.hoisted(() => ({ api: { list: vi.fn(), show: vi.fn() } }))
vi.mock('../services/taskSessions.js', () => ({ sessionsApi: api }))
import { useTaskSessions } from './useTaskSessions.js'

const deferred = () => { let resolve; const promise = new Promise(r => { resolve = r }); return { promise, resolve } }
beforeEach(() => { api.list.mockReset(); api.show.mockReset(); api.list.mockResolvedValue({ sessions: [{ id: 1, rev: 1 }, { id: 2, rev: 1 }] }) })
afterEach(cleanup)

describe('useTaskSessions events', () => {
  it('clears the events when another session is chosen and drops a late answer for the old one', async () => {
    const a = deferred()
    api.show.mockImplementation((id) => (id === 1 ? Promise.resolve({ events: [{ id: 'a1' }] }) : id === 2 ? a.promise : new Promise(() => {})))
    const { result, rerender } = renderHook(({ id }) => useTaskSessions(true, id), { initialProps: { id: 1 } })
    await waitFor(() => expect(result.current.events).toEqual([{ id: 'a1' }]))
    rerender({ id: 2 })
    await waitFor(() => expect(result.current.events).toEqual([]))
    rerender({ id: 3 })
    await act(async () => { a.resolve({ events: [{ id: 'b1' }] }) })
    expect(result.current.events).toEqual([])
  })
})
