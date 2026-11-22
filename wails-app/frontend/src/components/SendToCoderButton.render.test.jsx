// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup, act } from '@testing-library/react'
import '../i18n.js'

const { aitaskApi } = vi.hoisted(() => ({
  aitaskApi: { status: vi.fn(), submit: vi.fn(), startFill: vi.fn(), get: vi.fn(), cancel: vi.fn() },
}))
vi.mock('./SendToCoderApi.js', async (orig) => ({ ...(await orig()), aitaskApi }))
// Cancelling a task now asks first: these tests answer yes (the no path is in TaskHeaderCard.cancel.render.test.jsx).
vi.mock('./ConfirmDialog.jsx', () => ({ confirm: vi.fn(() => Promise.resolve(true)) }))

import SendToCoderButton from './SendToCoderButton.jsx'
import { TaskHeaderCard } from './bubbles/TaskHeaderCard.jsx'
import { CoderBubbleContext } from './bubbles/CoderBubbleContext.jsx'

const task = { taskId: 't1', conversationId: 'c1', status: 'running', goal: 'look at this' }

function renderButton(ctx = {}, props = {}) {
  const value = { openDraft: vi.fn(), navigate: vi.fn(), ...ctx }
  render(
    <CoderBubbleContext.Provider value={value}>
      <SendToCoderButton goal="look at this" context={['posting=/p.txt']} applicationId="app-1" origin={{ page: 'applications', label: 'Applications' }} {...props} />
    </CoderBubbleContext.Provider>,
  )
  return value
}

beforeEach(() => { vi.clearAllMocks() })
afterEach(() => { cleanup(); vi.useRealTimers() })

describe('SendToCoderButton task kind', () => {
  it("passes the started task's kind, so an application-apply chat is confined when it opens", async () => {
    aitaskApi.status.mockResolvedValue({ enabled: true })
    aitaskApi.startFill.mockResolvedValue({ ...task, kind: 'application-apply' })
    const ctx = renderButton({}, { kind: 'application-apply', start: () => aitaskApi.startFill('app-1') })
    fireEvent.click(screen.getByRole('button', { name: /send to coder/i }))
    await waitFor(() => expect(ctx.openDraft).toHaveBeenCalledTimes(1))
    expect(ctx.openDraft.mock.calls[0][0].task.kind).toBe('application-apply')
  })
})

describe('SendToCoderButton', () => {
  it('coder disabled: shows why and a Settings link, and never submits', async () => {
    aitaskApi.status.mockResolvedValue({ enabled: false, reason: 'Coder mode is off.' })
    const ctx = renderButton()
    fireEvent.click(screen.getByRole('button', { name: /send to coder/i }))
    expect(await screen.findByText(/Coder mode is off\./)).toBeInTheDocument()
    expect(screen.getByText(/still runs the regular way/)).toBeInTheDocument()
    fireEvent.click(screen.getByText('Open Settings'))
    expect(ctx.navigate).toHaveBeenCalledWith('settings')
    expect(aitaskApi.submit).not.toHaveBeenCalled()
    expect(ctx.openDraft).not.toHaveBeenCalled()
  })

  it('enabled: submits, then opens the bubble for the returned conversation with the task header', async () => {
    aitaskApi.status.mockResolvedValue({ enabled: true, reason: '' })
    aitaskApi.submit.mockResolvedValue(task)
    const ctx = renderButton()
    fireEvent.click(screen.getByRole('button', { name: /send to coder/i }))
    await waitFor(() => expect(ctx.openDraft).toHaveBeenCalledTimes(1))
    expect(aitaskApi.submit).toHaveBeenCalledWith('generic', 'look at this', ['posting=/p.txt'], 'app-1')
    expect(ctx.openDraft).toHaveBeenCalledWith({
      conversationId: 'c1',
      task: { taskId: 't1', kind: 'generic', goal: 'look at this', status: 'running', origin: { page: 'applications', label: 'Applications' } },
    })
  })

  it('a coder_disabled fallback from submit shows the reason and opens nothing', async () => {
    aitaskApi.status.mockResolvedValue({ enabled: true })
    aitaskApi.submit.mockResolvedValue({ fallback: 'oneshot', reason: 'turned off just now' })
    const ctx = renderButton()
    fireEvent.click(screen.getByRole('button', { name: /send to coder/i }))
    expect(await screen.findByText(/turned off just now/)).toBeInTheDocument()
    expect(ctx.openDraft).not.toHaveBeenCalled()
  })

  it('a failed submit is shown as an alert and opens nothing', async () => {
    aitaskApi.status.mockResolvedValue({ enabled: true })
    aitaskApi.submit.mockRejectedValue(new Error('context refused'))
    const ctx = renderButton()
    fireEvent.click(screen.getByRole('button', { name: /send to coder/i }))
    expect(await screen.findByRole('alert')).toHaveTextContent('context refused')
    expect(ctx.openDraft).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: /send to coder/i })).toBeEnabled()
  })

  it('a double click submits once', async () => {
    aitaskApi.status.mockResolvedValue({ enabled: true })
    let release
    aitaskApi.submit.mockReturnValue(new Promise(r => { release = r }))
    renderButton()
    const b = screen.getByRole('button', { name: /send to coder/i })
    fireEvent.click(b)
    fireEvent.click(b)
    await waitFor(() => expect(aitaskApi.submit).toHaveBeenCalledTimes(1))
    await act(async () => { release(task) })
  })

  it('evaluates goal and context thunks at click time', async () => {
    aitaskApi.status.mockResolvedValue({ enabled: true })
    aitaskApi.submit.mockResolvedValue(task)
    renderButton({}, { goal: () => 'late goal', context: () => ['selection-text=boom'] })
    fireEvent.click(screen.getByRole('button', { name: /send to coder/i }))
    await waitFor(() => expect(aitaskApi.submit).toHaveBeenCalledWith('generic', 'late goal', ['selection-text=boom'], 'app-1'))
  })
})

describe('TaskHeaderCard', () => {
  const t = { taskId: 't1', goal: 'look at this', status: 'running', origin: { page: 'applications', label: 'Applications' } }

  it('shows the ask, status, a way back to the tab and Cancel', async () => {
    aitaskApi.cancel.mockResolvedValue({ status: 'cancelled' })
    const onNavigate = vi.fn()
    render(<TaskHeaderCard task={t} onNavigate={onNavigate} />)
    expect(screen.getByText('look at this')).toBeInTheDocument()
    expect(screen.getByText('running')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Open in Applications' }))
    expect(onNavigate).toHaveBeenCalledWith('applications')
    fireEvent.click(screen.getByRole('button', { name: 'Cancel task' }))
    expect(await screen.findByText('cancelled')).toBeInTheDocument()
    expect(aitaskApi.cancel).toHaveBeenCalledWith('t1')
    expect(screen.queryByRole('button', { name: 'Cancel task' })).toBeNull()
  })

  it('a refused cancel is shown', async () => {
    aitaskApi.cancel.mockRejectedValue(new Error('operator only'))
    render(<TaskHeaderCard task={t} onNavigate={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: 'Cancel task' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('operator only')
  })

  it('polls the status while the task runs and stops when it is done', async () => {
    vi.useFakeTimers()
    aitaskApi.get.mockResolvedValue({ status: 'complete' })
    render(<TaskHeaderCard task={t} onNavigate={() => {}} />)
    await act(async () => { await vi.advanceTimersByTimeAsync(4100) })
    expect(screen.getByText('complete')).toBeInTheDocument()
    const calls = aitaskApi.get.mock.calls.length
    await act(async () => { await vi.advanceTimersByTimeAsync(12000) })
    expect(aitaskApi.get.mock.calls.length).toBe(calls)
    expect(screen.queryByRole('button', { name: 'Cancel task' })).toBeNull()
  })

  it('says the done label, not just "complete", for a task that has one', async () => {
    vi.useFakeTimers()
    aitaskApi.get.mockResolvedValue({ status: 'complete' })
    render(<TaskHeaderCard task={{ ...t, doneLabel: 'filled, waiting for you to submit' }} onNavigate={() => {}} />)
    await act(async () => { await vi.advanceTimersByTimeAsync(4100) })
    expect(screen.getByText('filled, waiting for you to submit')).toBeInTheDocument()
  })
})
