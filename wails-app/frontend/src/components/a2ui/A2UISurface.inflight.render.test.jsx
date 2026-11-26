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
