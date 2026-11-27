// @vitest-environment jsdom
// A press may start a send that takes minutes: until it answers, the card
// takes no second press and says it is working.
import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { render, renderHook, screen, fireEvent, act, cleanup } from '@testing-library/react'
import '../../i18n.js'

const { api, notify } = vi.hoisted(() => ({ api: { chatUIAction: vi.fn() }, notify: vi.fn() }))
vi.mock('../../services/api.js', () => ({ api, notify }))

import { A2UISurface } from './A2UISurface.jsx'
import { useA2UIActions } from './useA2UIActions.js'
import { applySurfaceEvent } from '../../lib/a2uiModel.js'
import { hilApproval } from './fixtures.js'

beforeEach(() => vi.useFakeTimers())
afterEach(() => { cleanup(); vi.useRealTimers(); vi.clearAllMocks() })

const card = () => applySurfaceEvent([], hilApproval[0], 1)[0]
const approve = () => screen.getByRole('button', { name: 'Approve and send' })

describe('a press in flight', () => {
  it('stays locked until it answers, whatever the card does, then for the usual moment', async () => {
    let answer
    const onAction = vi.fn(() => new Promise(r => { answer = r }))
    const surface = card()
    const { rerender } = render(<A2UISurface surface={surface} onAction={onAction} />)
    fireEvent.click(approve())
    await act(() => vi.advanceTimersByTimeAsync(10 * 60 * 1000))
    rerender(<A2UISurface surface={{ ...surface, dataModel: { ...surface.dataModel, tick: 1 }, lastSeq: 9 }} onAction={onAction} />)
    expect(approve().getAttribute('aria-disabled')).toBe('true')
    expect(screen.getByText('Working. Do not press again: this can take a while.')).toBeTruthy()
    fireEvent.click(approve())
    expect(onAction).toHaveBeenCalledTimes(1)
    await act(async () => { answer() })
    expect(approve().getAttribute('aria-disabled')).toBe('true')
    await act(() => vi.advanceTimersByTimeAsync(3100))
    expect(approve().getAttribute('aria-disabled')).toBeNull()
  })

  it('reads the journal while the command runs, so the in-progress card shows, and stops when it ends', async () => {
    let answer
    api.chatUIAction.mockReturnValue(new Promise(r => { answer = r }))
    const onChanged = vi.fn()
    const { result } = renderHook(() => useA2UIActions({ conversationId: 'c', send: vi.fn(), streaming: false, onChanged }))
    let done
    act(() => { done = result.current.onA2UIAction({ version: '1.0', action: {} }) })
    await act(() => vi.advanceTimersByTimeAsync(5000))
    expect(onChanged.mock.calls.length).toBeGreaterThanOrEqual(2)
    await act(async () => { answer({ outcome: 'accepted', delivered: 'system' }); await done })
    const after = onChanged.mock.calls.length
    await act(() => vi.advanceTimersByTimeAsync(20000))
    expect(onChanged).toHaveBeenCalledTimes(after)
    expect(notify).not.toHaveBeenCalled()
  })
})

// A press belongs to the chat it was made in.
describe('a press and a chat switch', () => {
  const mount = props => renderHook(p => useA2UIActions(p), { initialProps: props })
  const base = { streaming: false, onChanged: vi.fn() }
  const next = { outcome: 'accepted', delivered: 'next_turn', message: 'from chat A' }

  it('an answer that arrives after a switch waits for its own chat', async () => {
    let answer
    api.chatUIAction.mockReturnValue(new Promise(r => { answer = r }))
    const sendA = vi.fn()
    const sendB = vi.fn()
    const { result, rerender } = mount({ ...base, conversationId: 'a', send: sendA })
    let done
    act(() => { done = result.current.onA2UIAction({}) })
    rerender({ ...base, conversationId: 'b', send: sendB })
    await act(async () => { answer(next); await done })
    expect(sendB).not.toHaveBeenCalled()
    expect(sendA).not.toHaveBeenCalled()
    rerender({ ...base, conversationId: 'a', send: sendA })
    expect(sendA).toHaveBeenCalledWith('from chat A')
    expect(sendB).not.toHaveBeenCalled()
  })

  it('a held message is not sent into another chat', async () => {
    api.chatUIAction.mockResolvedValue(next)
    const sendA = vi.fn()
    const sendB = vi.fn()
    const { result, rerender } = mount({ ...base, streaming: true, conversationId: 'a', send: sendA })
    await act(() => result.current.onA2UIAction({}))
    rerender({ ...base, conversationId: 'b', send: sendB })
    expect(sendB).not.toHaveBeenCalled()
    rerender({ ...base, streaming: true, conversationId: 'a', send: sendA })
    expect(sendA).not.toHaveBeenCalled()
    rerender({ ...base, conversationId: 'a', send: sendA })
    expect(sendA).toHaveBeenCalledTimes(1)
    expect(sendB).not.toHaveBeenCalled()
  })
})

