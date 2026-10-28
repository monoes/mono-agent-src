// @vitest-environment jsdom
// A running org as a bubble (#229): the stage draws the org from its bus,
// the chat shows the boss thread `org chat history` prints, questions and
// gates are answered inline exactly once, a stopped org's items can't be
// answered, and pause/stop ask first.
import React, { useEffect } from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup, act, within, configure } from '@testing-library/react'

// The first render in a file costs about 3 s while modules warm up (0.5-1 s after), so the 1 s waitFor
// and 5 s test defaults fail on a loaded machine without anything being wrong.
configure({ asyncUtilTimeout: 8000 })
vi.setConfig({ testTimeout: 20000 })
import '../../i18n.js'
import history from './__fixtures__/acme-history.json'
// The bus that history was built from (internal/orgchat/testdata, kept in
// step by the CLI's TestOrgChatHistoryGUIFixture).
import BUS from './__fixtures__/acme-bus.json'

const { api, listeners, confirmMock } = vi.hoisted(() => ({
  listeners: { org: new Set(), run: new Set() },
  confirmMock: vi.fn(),
  api: {
    getOrgChatHistory: vi.fn(),
    getOrgLogs: vi.fn(),
    getOrgDesign: vi.fn(),
    getOrgStatus: vi.fn(),
    sendOrgChat: vi.fn(),
    answerOrgChat: vi.fn(),
    resolveOrgChat: vi.fn(),
    controlOrg: vi.fn(),
    streamOrgEvents: vi.fn(() => Promise.resolve({ ok: true })),
    stopOrgEvents: vi.fn(() => Promise.resolve({ ok: true })),
    getChatTurns: vi.fn(),
    coderStatus: vi.fn(),
  },
}))
vi.mock('../../services/api.js', () => ({
  api,
  notify: vi.fn(),
  onChatEvent: () => () => {},
  onOrgEvent: cb => { listeners.org.add(cb); return () => listeners.org.delete(cb) },
  onOrgRunStatus: cb => { listeners.run.add(cb); return () => listeners.run.delete(cb) },
  newOrgEventsStreamId: (() => { let n = 0; return () => `s${++n}` })(),
}))
vi.mock('../ConfirmDialog.jsx', () => ({ confirm: confirmMock }))

import CoderBubbles from './CoderBubbles.jsx'
import { useCoderBubbles } from './useCoderBubbles.js'
import { orgEventHolders } from '../../lib/orgEventStreams.js'

let storeRef = null
function Harness({ org = 'acme', expand = true }) {
  const store = useCoderBubbles()
  storeRef = store
  useEffect(() => { store.openOrg(org, { expand }) }, []) // eslint-disable-line react-hooks/exhaustive-deps
  return <CoderBubbles store={store} onNavigate={() => {}} />
}

const node = id => document.querySelector(`[data-testid="stage-node"][data-agent="${id}"]`)
const emit = (event) => act(() => { for (const cb of listeners.org) cb({ orgName: 'acme', event }) })

function deferred() {
  let resolve
  const promise = new Promise(r => { resolve = r })
  return { promise, resolve }
}

beforeEach(() => {
  vi.clearAllMocks()
  listeners.org.clear()
  localStorage.clear()
  Element.prototype.scrollTo = function scrollTo() {}
  api.getOrgChatHistory.mockResolvedValue(history)
  api.getOrgLogs.mockResolvedValue({ v: 1, items: BUS })
  api.getOrgDesign.mockResolvedValue({ org: { roles: history.roles } })
  api.coderStatus.mockResolvedValue({ enabled: false })
  confirmMock.mockResolvedValue(true)
})
afterEach(() => { cleanup(); storeRef = null })

describe('an org bubble', () => {
  it('draws the org on the stage with the boss as the lead, and flags who waits on you', async () => {
    render(<Harness />)
    expect(await screen.findByTestId('org-bubble-overlay')).toBeInTheDocument()
    await waitFor(() => expect(node('qa')).toBeTruthy())
    expect(node('lead')).toHaveTextContent('Chief')
    expect(node('dev')).toHaveTextContent('Developer')
    // The boss's gate and qa's question are pending; dev's approval was granted.
    expect(node('lead').querySelector('[data-testid="stage-needs-you"]')).toBeTruthy()
    expect(node('qa').querySelector('[data-testid="stage-needs-you"]')).toBeTruthy()
    expect(node('dev').querySelector('[data-testid="stage-needs-you"]')).toBeNull()
    expect(screen.getByTestId('org-bubble-status')).toHaveAttribute('data-status', 'running')
    // A role's drawer shows its own tool calls, and no Stop (roles stop with the org).
    fireEvent.click(node('dev'))
    const drawer = await screen.findByTestId('stage-drawer')
    expect(drawer.querySelector('[data-testid="stage-tools"]')).toBeTruthy()
    expect(drawer.querySelector('[data-testid="stage-stop"]')).toBeNull()
    // The bubble holds the org's event tail while it is open.
    expect(orgEventHolders('acme')).toBe(1)
    expect(api.streamOrgEvents).toHaveBeenCalledWith('acme', expect.any(String))
  })

  it('shows the boss thread: your message, the boss, team rows, and pending items to act on', async () => {
    render(<Harness />)
    const thread = await screen.findByTestId('org-chat-thread')
    await waitFor(() => expect(thread).toHaveTextContent('Please ship the release notes today.'))
    expect(thread).toHaveTextContent("On it. I'm asking dev to draft them.")
    expect(screen.getAllByTestId('org-chat-team')).toHaveLength(3)
    // Team rows stay collapsed until opened.
    expect(thread).not.toHaveTextContent('Draft ready in NOTES.md')
    const questions = screen.getAllByTestId('org-chat-question')
    expect(questions.map(q => q.dataset.pending)).toEqual(['false', 'true', 'true'])
    expect(questions[0]).toHaveTextContent('Answer: 1.2.0')
    expect(screen.getByTestId('org-chat-gate')).toHaveAttribute('data-pending', 'true')
    expect(screen.getByTestId('org-chat-approval')).toHaveAttribute('data-pending', 'false')
  })

  it('answers a question once, however often it is clicked', async () => {
    const pending = deferred()
    api.answerOrgChat.mockReturnValue(pending.promise)
    render(<Harness />)
    await waitFor(() => expect(screen.getAllByTestId('org-chat-question')).toHaveLength(3))
    const card = screen.getAllByTestId('org-chat-question')[1]
    fireEvent.change(card.querySelector('input'), { target: { value: 'Yes, run e2e' } })
    const button = card.querySelector('[data-testid="org-chat-answer"]')
    fireEvent.click(button)
    fireEvent.click(button)
    fireEvent.keyDown(card.querySelector('input'), { key: 'Enter' })
    expect(api.answerOrgChat).toHaveBeenCalledTimes(1)
    expect(api.answerOrgChat).toHaveBeenCalledWith('acme', 'q-2250-cd34', 'Yes, run e2e')
    await waitFor(() => expect(button).toBeDisabled())
    await act(async () => { pending.resolve({ ok: true, state: 'answered', already: false }) })
    await waitFor(() => expect(screen.getAllByTestId('org-chat-question')[1]).toHaveAttribute('data-pending', 'false'))
    expect(screen.getAllByTestId('org-chat-question')[1]).toHaveTextContent('answered')
  })

  it('shows a crashed run as crashed, with why', async () => {
    api.getOrgChatHistory.mockResolvedValue({ ...history, status: 'crashed', status_error: 'pid 4242 gone' })
    render(<Harness />)
    await waitFor(() => expect(screen.getByTestId('org-bubble-status')).toHaveAttribute('data-status', 'crashed'))
    const badge = screen.getByTestId('org-bubble-status')
    expect(badge).toHaveTextContent('crashed')
    expect(badge).toHaveAttribute('title', 'pid 4242 gone')
  })

  it('offers Dismiss only on a blocking question, and dismisses it once', async () => {
    const items = history.items.map(it => (it.ref === 'q-2250-cd34' ? { ...it, blocking: true } : it))
    api.getOrgChatHistory.mockResolvedValue({ ...history, items })
    api.dismissOrgChat = vi.fn().mockResolvedValue({ ok: true, state: 'dismissed', already: false })
    render(<Harness />)
    await waitFor(() => expect(screen.getAllByTestId('org-chat-question')).toHaveLength(3))
    const cards = screen.getAllByTestId('org-chat-question')
    expect(cards[2].querySelector('[data-testid="org-chat-dismiss"]')).toBeNull()
    const button = cards[1].querySelector('[data-testid="org-chat-dismiss"]')
    fireEvent.click(button)
    fireEvent.click(button)
    expect(api.dismissOrgChat).toHaveBeenCalledTimes(1)
    expect(api.dismissOrgChat).toHaveBeenCalledWith('acme', 'q-2250-cd34', '')
    await waitFor(() => expect(screen.getAllByTestId('org-chat-question')[1]).toHaveAttribute('data-pending', 'false'))
    expect(screen.getAllByTestId('org-chat-question')[1]).toHaveTextContent('dismissed')
  })

  it('says so when an item was already resolved elsewhere', async () => {
    api.resolveOrgChat.mockResolvedValue({ ok: true, kind: 'gate', ref: 'gate-1850-x1', state: 'rejected', already: true })
    render(<Harness />)
    const gate = await screen.findByTestId('org-chat-gate')
    fireEvent.change(gate.querySelector('input'), { target: { value: 'ship it' } })
    fireEvent.click(gate.querySelector('[data-testid="org-chat-approve"]'))
    expect(api.resolveOrgChat).toHaveBeenCalledWith('acme', 'gate-1850-x1', true, 'ship it')
    await waitFor(() => expect(screen.getByTestId('org-chat-gate')).toHaveTextContent('Already rejected'))
  })

  it('shows the CLI\'s refusal and keeps the item pending', async () => {
    api.resolveOrgChat.mockResolvedValue({ error: 'org "acme" is not running (status: stopped); nothing was sent' })
    render(<Harness />)
    const gate = await screen.findByTestId('org-chat-gate')
    fireEvent.click(gate.querySelector('[data-testid="org-chat-deny"]'))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('nothing was sent'))
    expect(screen.getByTestId('org-chat-gate')).toHaveAttribute('data-pending', 'true')
  })

  it('disables answering while the org is stopped, but still lets you message it', async () => {
    const items = history.items.map(it => (it.ref === 'q-2250-cd34' ? { ...it, blocking: true } : it))
    api.getOrgChatHistory.mockResolvedValue({ ...history, items, status: 'stopped' })
    api.sendOrgChat.mockResolvedValue({ v: 1, org: 'acme', to: 'ceo', delivery: 'queued', messageId: 'msg-9' })
    render(<Harness />)
    expect(await screen.findByTestId('org-chat-stopped')).toBeInTheDocument()
    const gate = screen.getByTestId('org-chat-gate')
    expect(gate.querySelector('[data-testid="org-chat-approve"]')).toBeDisabled()
    expect(gate.querySelector('[data-testid="org-chat-deny"]')).toBeDisabled()
    expect(screen.queryByTestId('org-bubble-stop')).toBeNull()
    // A question can still be dismissed: monomind queues the note.
    const dismiss = screen.getAllByTestId('org-chat-question')[1].querySelector('[data-testid="org-chat-dismiss"]')
    expect(dismiss).toBeEnabled()
    const box = screen.getByPlaceholderText('Type a message...')
    fireEvent.change(box, { target: { value: 'Pick this up when you start' } })
    fireEvent.keyDown(box, { key: 'Enter' })
    await waitFor(() => expect(api.sendOrgChat).toHaveBeenCalledWith('acme', 'Pick this up when you start'))
    expect(await screen.findByText('Pick this up when you start')).toBeInTheDocument()
    expect(screen.getByText("queued for the org's next start")).toBeInTheDocument()
  })

  it('asks before pausing or stopping, and does nothing when you decline', async () => {
    api.controlOrg.mockResolvedValue({ v: 1, ok: true })
    render(<Harness />)
    fireEvent.click(await screen.findByTestId('org-bubble-pause'))
    await waitFor(() => expect(confirmMock).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(api.controlOrg).toHaveBeenCalledWith('acme', 'pause'))
    confirmMock.mockResolvedValue(false)
    fireEvent.click(screen.getByTestId('org-bubble-stop'))
    await waitFor(() => expect(confirmMock).toHaveBeenCalledTimes(2))
    expect(api.controlOrg).toHaveBeenCalledTimes(1)
  })

  it('folds live events into the stage and refetches the thread when it can change', async () => {
    render(<Harness />)
    await waitFor(() => expect(node('qa')).toBeTruthy())
    const calls = api.getOrgChatHistory.mock.calls.length
    emit({ id: 'run-a-2400-30', ts: 2400, org: 'acme', run: 'run-a', type: 'status', from: 'qa', reason: 'state-change', msg: 'idle → working', data: { from: 'idle', to: 'working' } })
    await waitFor(() => expect(node('qa')).toHaveAttribute('data-status', 'working'))
    emit({ id: 'run-a-2500-31', ts: 2500, org: 'acme', run: 'run-a', type: 'chat', from: 'ceo', msg: 'Thanks!' })
    await waitFor(() => expect(api.getOrgChatHistory.mock.calls.length).toBe(calls + 1), { timeout: 2000 })
  })

  it('collapses without touching the org, and closing the bubble releases its tail', async () => {
    render(<Harness />)
    await screen.findByTestId('org-bubble-overlay')
    fireEvent.keyDown(window, { key: 'Escape' })
    await waitFor(() => expect(screen.queryByTestId('org-bubble-overlay')).toBeNull())
    expect(api.controlOrg).not.toHaveBeenCalled()
    const bubble = document.querySelector('[data-bubble="org:acme"]')
    expect(bubble).toBeTruthy()
    // Collapsed, it pulses amber: items wait on the person.
    await waitFor(() => expect(bubble).toHaveAttribute('data-status', 'needs'))
    fireEvent.click(screen.getByRole('button', { name: /Close acme/ }))
    await waitFor(() => expect(document.querySelector('[data-bubble="org:acme"]')).toBeNull())
    expect(orgEventHolders('acme')).toBe(0)
    expect(api.stopOrgEvents).toHaveBeenCalled()
    expect(api.controlOrg).not.toHaveBeenCalled()
  })

  it('takes focus into the dialog at once, and into the composer once the thread has loaded (#477)', async () => {
    const pending = deferred()
    api.getOrgChatHistory.mockReturnValue(pending.promise)
    render(<Harness />)
    const panel = await screen.findByTestId('org-bubble-overlay')
    const composer = panel.querySelector('textarea')
    expect(composer).toBeDisabled()
    expect(panel.contains(document.activeElement)).toBe(true)
    await act(async () => { pending.resolve(history) })
    await waitFor(() => expect(document.activeElement).toBe(composer))
  })

  it('leaves focus where the person put it while the thread was loading', async () => {
    const pending = deferred()
    api.getOrgChatHistory.mockReturnValue(pending.promise)
    render(<Harness />)
    const panel = await screen.findByTestId('org-bubble-overlay')
    const tab = panel.querySelector('[data-thread-tab="team"]')
    tab.focus()
    await act(async () => { pending.resolve(history) })
    await waitFor(() => expect(panel.querySelector('textarea')).not.toBeDisabled())
    expect(document.activeElement).toBe(tab)
  })

  it('keeps the header on one row at a narrow width: titles shrink, the buttons stay (#477)', async () => {
    const wide = window.innerWidth
    window.innerWidth = 480
    try {
      render(<Harness />)
      const panel = await screen.findByTestId('org-bubble-overlay')
      await waitFor(() => expect(screen.getByTestId('org-bubble-pause')).toBeInTheDocument())
      const header = panel.firstElementChild
      expect(header.style.flexWrap).toBe('nowrap')
      const close = within(header).getByRole('button', { name: /Close this bubble/ })
      for (const b of within(header).getAllByRole('button')) expect(b.style.flexShrink).toBe('0')
      expect(close.closest('[data-testid="org-bubble-overlay"] > div')).toBe(header)
      // Narrow, the text buttons are icons, named for screen readers.
      for (const id of ['org-bubble-pause', 'org-bubble-stop']) {
        const b = screen.getByTestId(id)
        expect(b.textContent.trim()).toBe('')
        expect(b).toHaveAccessibleName()
      }
      const title = within(header).getByText('acme')
      expect(title.style.minWidth).toBe('0px')
    } finally {
      window.innerWidth = wide
    }
  })

  it('is kept across restarts and opens collapsed when asked', async () => {
    render(<Harness expand={false} />)
    await waitFor(() => expect(document.querySelector('[data-bubble="org:acme"]')).toBeTruthy())
    expect(screen.queryByTestId('org-bubble-overlay')).toBeNull()
    await waitFor(() => expect(localStorage.getItem('monoagent:coderBubbles:v1')).toBeTruthy())
    const saved = JSON.parse(localStorage.getItem('monoagent:coderBubbles:v1'))
    expect(saved.bubbles).toEqual([{ kind: 'org', orgName: 'acme', runId: '', finished: false }])
    expect(storeRef.bubbles[0]).toMatchObject({ key: 'org:acme', kind: 'org', orgName: 'acme' })
  })
})
