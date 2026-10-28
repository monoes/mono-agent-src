// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup, act } from '@testing-library/react'

const { App, aitaskApi } = vi.hoisted(() => ({
  App: {
    GetApplicationTab: vi.fn(), ConfirmApplicationSubmitted: vi.fn(), ResumeApplicationFill: vi.fn(),
    StartApplicationQueue: vi.fn(), GetApplicationAssets: vi.fn(), SweepApplicationTabs: vi.fn(), OpenPathWithOS: vi.fn(),
  },
  aitaskApi: { status: vi.fn(), submit: vi.fn(), startFill: vi.fn(), get: vi.fn(), cancel: vi.fn() },
}))
vi.mock('../../wailsjs/go/main/App', () => App)
vi.mock('../SendToCoderApi.js', async (orig) => ({ ...(await orig()), aitaskApi }))

import ApplicationParkedPanel from './ApplicationParkedPanel.jsx'
import ApplyQueueButton from './ApplyQueueButton.jsx'
import { CoderBubbleContext } from '../bubbles/CoderBubbleContext.jsx'

const openDraft = vi.fn()
const wrap = (ui) => <CoderBubbleContext.Provider value={{ openDraft, navigate: vi.fn() }}>{ui}</CoderBubbleContext.Provider>
const assets = { fill: { screenshot: '/shots/a1.png', note: '' } }
const parked = { applicationId: 'a1', tab: { tabId: 7, origin: 'https://jobs.example', state: 'parked' }, submitObservedAt: '' }

beforeEach(() => {
  vi.clearAllMocks()
  App.GetApplicationTab.mockResolvedValue(parked)
  aitaskApi.status.mockResolvedValue({ enabled: true })
})
afterEach(cleanup)

describe('ApplicationParkedPanel', () => {
  it('renders nothing for other statuses', () => {
    for (const status of ['pending', 'applied', 'rejected']) {
      const { container, unmount } = render(wrap(<ApplicationParkedPanel detail={{ id: 'a1', status }} assets={assets} />))
      expect(container).toBeEmptyDOMElement()
      unmount()
    }
    expect(App.GetApplicationTab).not.toHaveBeenCalled()
  })

  it('awaiting_submit: waits for the person, shows the tab origin and screenshot, no confirm yet', async () => {
    render(wrap(<ApplicationParkedPanel detail={{ id: 'a1', status: 'awaiting_submit' }} assets={assets} />))
    expect(screen.getByText('Waiting for your submit')).toBeInTheDocument()
    expect(await screen.findByText(/Tab: https:\/\/jobs\.example/)).toBeInTheDocument()
    expect(App.GetApplicationTab).toHaveBeenCalledWith('a1')
    expect(screen.queryByRole('button', { name: 'Confirm' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: /screenshot/ }))
    expect(App.OpenPathWithOS).toHaveBeenCalledWith('/shots/a1.png')
  })

  it('a seen submit asks, Confirm calls the send path and refreshes, and the copy never claims it was submitted', async () => {
    App.GetApplicationTab.mockResolvedValue({ ...parked, submitObservedAt: '2026-10-22T10:00:00Z' })
    App.ConfirmApplicationSubmitted.mockResolvedValue(undefined)
    const onChanged = vi.fn()
    const { container } = render(wrap(<ApplicationParkedPanel detail={{ id: 'a1', status: 'awaiting_submit' }} assets={assets} onChanged={onChanged} />))
    expect(await screen.findByText(/Did you send it\?/)).toBeInTheDocument()
    expect(container.textContent).not.toMatch(/submitted/i)
    fireEvent.click(screen.getByRole('button', { name: 'Confirm' }))
    await waitFor(() => expect(onChanged).toHaveBeenCalled())
    expect(App.ConfirmApplicationSubmitted).toHaveBeenCalledWith('a1')
  })

  it('Not yet hides the question and confirms nothing', async () => {
    App.GetApplicationTab.mockResolvedValue({ ...parked, submitObservedAt: 'x' })
    render(wrap(<ApplicationParkedPanel detail={{ id: 'a1', status: 'awaiting_submit' }} assets={assets} />))
    fireEvent.click(await screen.findByRole('button', { name: 'Not yet' }))
    expect(screen.queryByText(/Did you send it\?/)).toBeNull()
    expect(App.ConfirmApplicationSubmitted).not.toHaveBeenCalled()
  })

  it('a refused confirm is shown and the application is not refreshed as applied', async () => {
    App.GetApplicationTab.mockResolvedValue({ ...parked, submitObservedAt: 'x' })
    App.ConfirmApplicationSubmitted.mockRejectedValue(new Error('invalid transition'))
    const onChanged = vi.fn()
    render(wrap(<ApplicationParkedPanel detail={{ id: 'a1', status: 'awaiting_submit' }} assets={assets} onChanged={onChanged} />))
    fireEvent.click(await screen.findByRole('button', { name: 'Confirm' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('invalid transition')
    expect(onChanged).not.toHaveBeenCalled()
  })

  it('a failed tab read still shows the waiting state', async () => {
    App.GetApplicationTab.mockRejectedValue(new Error('no cli'))
    render(wrap(<ApplicationParkedPanel detail={{ id: 'a1', status: 'awaiting_submit' }} assets={assets} />))
    expect(screen.getByText('Waiting for your submit')).toBeInTheDocument()
  })

  it('needs_you: shows the reason, and Reopen and refill resumes and opens the task chat', async () => {
    App.ResumeApplicationFill.mockResolvedValue({ taskId: 't2', conversationId: 'c2', status: 'running', goal: 'refill' })
    render(wrap(<ApplicationParkedPanel detail={{ id: 'a1', status: 'needs_you' }} assets={{ fill: { note: 'A CAPTCHA blocked the form' } }} />))
    expect(screen.getByText('A CAPTCHA blocked the form')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Reopen and refill' }))
    await waitFor(() => expect(openDraft).toHaveBeenCalledTimes(1))
    expect(App.ResumeApplicationFill).toHaveBeenCalledWith('a1')
    expect(openDraft.mock.calls[0][0]).toMatchObject({ conversationId: 'c2', task: { taskId: 't2' } })
  })

  it('needs_you with coder off: says why and resumes nothing', async () => {
    aitaskApi.status.mockResolvedValue({ enabled: false, reason: 'Coder mode is off.' })
    render(wrap(<ApplicationParkedPanel detail={{ id: 'a1', status: 'needs_you' }} assets={{ fill: {} }} />))
    fireEvent.click(screen.getByRole('button', { name: 'Reopen and refill' }))
    expect(await screen.findByText(/Coder mode is off\./)).toBeInTheDocument()
    expect(App.ResumeApplicationFill).not.toHaveBeenCalled()
  })
})

describe('ApplyQueueButton', () => {
  it('is disabled with nothing selected', () => {
    render(<ApplyQueueButton ids={[]} />)
    expect(screen.getByRole('button', { name: /Apply selected with coder/ })).toBeDisabled()
  })

  it('starts the queue for the selected ids with the saved max-parked setting (0)', async () => {
    App.StartApplicationQueue.mockResolvedValue(undefined)
    const onStarted = vi.fn()
    render(<ApplyQueueButton ids={['a1', 'a2']} onStarted={onStarted} />)
    fireEvent.click(screen.getByRole('button', { name: 'Apply selected with coder (2)' }))
    await waitFor(() => expect(onStarted).toHaveBeenCalled())
    expect(App.StartApplicationQueue).toHaveBeenCalledWith(['a1', 'a2'], 0)
    expect(screen.getByRole('status')).toHaveTextContent(/check and submit/)
  })

  it('docks a collapsed bubble for each queued application once its fill has a chat', async () => {
    vi.useFakeTimers()
    try {
      App.StartApplicationQueue.mockResolvedValue(undefined)
      App.GetApplicationAssets
        .mockResolvedValueOnce({ task: null })
        .mockResolvedValueOnce({ task: { taskId: 't1', conversationId: 'c1', status: 'running' } })
        .mockResolvedValue({ task: null })
      render(wrap(<ApplyQueueButton ids={['a1']} />))
      await act(async () => { fireEvent.click(screen.getByRole('button', { name: /Apply selected/ })) })
      await act(async () => { await vi.advanceTimersByTimeAsync(4000) })
      expect(openDraft).not.toHaveBeenCalled()
      await act(async () => { await vi.advanceTimersByTimeAsync(4000) })
      expect(openDraft).toHaveBeenCalledWith(expect.objectContaining({ conversationId: 'c1', collapsed: true }))
    } finally { vi.useRealTimers() }
  })

  it('coder off: says why and starts nothing', async () => {
    aitaskApi.status.mockResolvedValue({ enabled: false, reason: 'Coder mode is off.' })
    render(<ApplyQueueButton ids={['a1']} />)
    fireEvent.click(screen.getByRole('button', { name: /Apply selected/ }))
    expect(await screen.findByText(/Coder mode is off\./)).toBeInTheDocument()
    expect(App.StartApplicationQueue).not.toHaveBeenCalled()
  })

  it('a refused start is shown', async () => {
    App.StartApplicationQueue.mockRejectedValue(new Error('a fill queue is already running for this profile'))
    render(<ApplyQueueButton ids={['a1']} />)
    fireEvent.click(screen.getByRole('button', { name: /Apply selected/ }))
    expect(await screen.findByRole('alert')).toHaveTextContent('already running')
  })
})
