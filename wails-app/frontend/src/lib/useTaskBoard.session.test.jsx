// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook, act, cleanup } from '@testing-library/react'

const { sess } = vi.hoisted(() => ({ sess: { board: vi.fn(), pulse: vi.fn() } }))
vi.mock('../services/tasks.js', () => ({ tasksApi: {}, onTasksChanged: () => () => {} }))
vi.mock('../services/taskSessions.js', () => ({ sessionsApi: sess }))
import { useTaskBoard } from './useTaskBoard.js'

beforeEach(() => { vi.useFakeTimers(); sess.board.mockReset(); sess.pulse.mockReset(); sess.board.mockResolvedValue({ rev: 1, counts: {}, tasks: {} }) })
afterEach(() => { cleanup(); vi.useRealTimers() })

describe('useTaskBoard session pulse', () => {
  it('reads again when the revision moved between the first read and the first tick', async () => {
    // The session moves to rev 2 as soon as the first board read has started.
    sess.pulse.mockImplementation(async () => ({ rev: sess.board.mock.calls.length ? 2 : 1 }))
    renderHook(() => useTaskBoard(true, 7))
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    expect(sess.board).toHaveBeenCalledTimes(1)
    await act(async () => { await vi.advanceTimersByTimeAsync(2100) })
    expect(sess.board).toHaveBeenCalledTimes(2)
    await act(async () => { await vi.advanceTimersByTimeAsync(2100) })
    expect(sess.board).toHaveBeenCalledTimes(2) // unchanged afterwards
  })
})
