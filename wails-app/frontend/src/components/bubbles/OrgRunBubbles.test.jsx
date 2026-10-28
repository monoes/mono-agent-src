// @vitest-environment jsdom
// One circle per org RUN: a live bubble is re-keyed to its run when the id
// arrives (same slot, nothing remounts), an org never has two live bubbles, a
// run that is over opens as a read-only bubble, and entries saved before
// per-run bubbles still load.
import React, { useEffect } from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { renderHook, render, screen, fireEvent, waitFor, act, cleanup } from '@testing-library/react'
import '../../i18n.js'
import history from './__fixtures__/acme-history.json'
import BUS from './__fixtures__/acme-bus.json'
import sendEmail from './__fixtures__/hil-send-email.json'

const { api, listeners } = vi.hoisted(() => ({
  listeners: { org: new Set(), run: new Set() },
  api: {
    getOrgSummary: vi.fn(), getOrgStatus: vi.fn(), listNeedsYou: vi.fn(), getOrgLogs: vi.fn(), getOrgDesign: vi.fn(),
    getOrgChatHistory: vi.fn(), sendOrgChat: vi.fn(), answerOrgChat: vi.fn(), controlOrg: vi.fn(), resolveOrgChat: vi.fn(),
    streamOrgEvents: vi.fn(() => Promise.resolve({ ok: true })), stopOrgEvents: vi.fn(() => Promise.resolve({ ok: true })),
    getChatTurns: vi.fn(), hilList: vi.fn(), coderStatus: vi.fn(),
  },
}))
vi.mock('../../services/api.js', () => ({
  api,
  notify: vi.fn(),
  onChatEvent: () => () => {},
  onOrgEvent: cb => { listeners.org.add(cb); return () => listeners.org.delete(cb) },
  onOrgRunStatus: cb => { listeners.run.add(cb); return () => listeners.run.delete(cb) },
  onOrgEventsClosed: () => () => {},
  newOrgEventsStreamId: (() => { let n = 0; return () => `s${++n}` })(),
}))

import CoderBubbles from './CoderBubbles.jsx'
import { useCoderBubbles } from './useCoderBubbles.js'
import { useOrgBubbleWatch } from './useOrgBubbleWatch.js'
import { loadState, saveState, STORAGE_KEY } from '../../lib/coderBubbles.js'

const runStatus = (orgName, status, runId) => act(() => { for (const cb of listeners.run) cb({ orgName, status, ...(runId ? { runId } : {}) }) })
const keys = r => r.current.bubbles.map(b => b.key)

beforeEach(() => {
  vi.clearAllMocks()
  for (const set of Object.values(listeners)) set.clear()
  localStorage.clear()
  Element.prototype.scrollTo = function scrollTo() {}
  api.getOrgSummary.mockResolvedValue({ orgs: [] })
  api.getOrgStatus.mockResolvedValue({ run: 'r1' })
  api.listNeedsYou.mockResolvedValue({ items: [] })
  api.getOrgLogs.mockResolvedValue({ v: 1, items: BUS })
  api.getOrgDesign.mockResolvedValue({ org: { roles: history.roles } })
  api.getOrgChatHistory.mockResolvedValue(history)
  api.getChatTurns.mockResolvedValue({ items: [] })
  api.hilList.mockResolvedValue([])
  api.coderStatus.mockResolvedValue({ enabled: false })
})
afterEach(() => cleanup())

describe('org run bubbles: key migration', () => {
  it('re-keys the docked bubble to its run when the id arrives, keeping its slot and one bubble', () => {
    const { result } = renderHook(() => { const store = useCoderBubbles(); useOrgBubbleWatch(store); return store })
    runStatus('acme', 'running')
    expect(keys(result)).toEqual(['org:acme'])
    const slot = result.current.bubbles[0].slot
    result.current.setView('org:acme', { draft: 'half typed' })
    runStatus('acme', 'running', 'r1')
    expect(keys(result)).toEqual(['org:acme#r1'])
    expect(result.current.bubbles[0]).toMatchObject({ runId: 'r1', finished: false, slot })
    expect(result.current.getView('org:acme#r1').draft).toBe('half typed')
    // A repeat of the same event changes nothing.
    runStatus('acme', 'running', 'r1')
    expect(keys(result)).toEqual(['org:acme#r1'])
  })

  it('keeps the expanded bubble expanded across the re-key', () => {
    const { result } = renderHook(() => useCoderBubbles())
    act(() => { result.current.openOrg('acme') })
    expect(result.current.expandedKey).toBe('org:acme')
    act(() => { result.current.openOrg('acme', { runId: 'r1', expand: false }) })
    expect(result.current.expandedKey).toBe('org:acme#r1')
    expect(keys(result)).toEqual(['org:acme#r1'])
  })

  it('never has two live bubbles for an org: a new run turns the earlier one into a finished run', () => {
    const { result } = renderHook(() => useCoderBubbles())
    act(() => { result.current.openOrg('acme', { runId: 'r1' }) })
    act(() => { result.current.openOrg('acme', { runId: 'r1' }) })
    expect(keys(result)).toEqual(['org:acme#r1'])
    act(() => { result.current.openOrg('acme', { runId: 'r2', expand: false }) })
    expect(result.current.bubbles.map(b => [b.key, b.finished])).toEqual([['org:acme#r1', true], ['org:acme#r2', false]])
    // Opening the live run again, or without an id, reuses the live one.
    act(() => { result.current.openOrg('acme') })
    act(() => { result.current.openOrg('acme', { runId: 'r2' }) })
    expect(result.current.bubbles.filter(b => !b.finished)).toHaveLength(1)
    expect(result.current.bubbles).toHaveLength(2)
  })

  it('a finished run opens beside the live one and is reused when opened again', async () => {
    const { result } = renderHook(() => useCoderBubbles())
    act(() => { result.current.openOrg('acme', { runId: 'r2' }) })
    act(() => { result.current.openOrg('acme', { runId: 'r1', finished: true }) })
    act(() => { result.current.openOrg('acme', { runId: 'r1', finished: true }) })
    expect(result.current.bubbles.map(b => [b.key, b.finished])).toEqual([['org:acme#r2', false], ['org:acme#r1', true]])
    await waitFor(() => expect(api.getOrgLogs).toHaveBeenCalledWith('acme', 'r1'))
    expect(api.getOrgLogs.mock.calls.filter(c => c[1] === 'r1')).toHaveLength(1)
    // Its summary waits on nobody.
    expect(result.current.summaryOf('org:acme#r1')).toMatchObject({ needs: 0 })
    expect(['done', 'error']).toContain(result.current.summaryOf('org:acme#r1').status)
  })

  it('counts a run once for the needs ring: a finished bubble never carries HIL asks', async () => {
    const { result } = renderHook(() => useCoderBubbles())
    act(() => { result.current.openOrg('acme', { runId: 'r1' }) })
    act(() => { result.current.setHilNeeds({ 'org:acme#r1': 2 }) })
    expect(result.current.summaryOf('org:acme#r1').needs).toBe(2)
    act(() => { result.current.openOrg('acme', { runId: 'r2', expand: false }) })
    expect(result.current.summaryOf('org:acme#r1').needs).toBe(0)
  })
})

describe('org run bubbles: persistence', () => {
  it('loads entries saved before per-run bubbles (no runId, no finished flag)', () => {
    localStorage.setItem(STORAGE_KEY, JSON.stringify({ bubbles: [{ kind: 'org', orgName: 'acme' }], side: 'left' }))
    const { bubbles, side } = loadState()
    expect(side).toBe('left')
    expect(bubbles).toHaveLength(1)
    expect(bubbles[0]).toMatchObject({ key: 'org:acme', kind: 'org', orgName: 'acme', runId: '', finished: false })
  })

  it('round-trips run ids and the finished flag, and tolerates junk', async () => {
    const { result } = renderHook(() => useCoderBubbles())
    await act(async () => {})
    act(() => { result.current.openOrg('acme', { runId: 'r2' }) })
    act(() => { result.current.openOrg('acme', { runId: 'r1', finished: true }) })
    const saved = JSON.parse(localStorage.getItem(STORAGE_KEY))
    expect(saved.bubbles).toEqual([
      { kind: 'org', orgName: 'acme', runId: 'r2', finished: false },
      { kind: 'org', orgName: 'acme', runId: 'r1', finished: true },
    ])
    expect(loadState().bubbles.map(b => b.key)).toEqual(['org:acme#r2', 'org:acme#r1'])
    localStorage.setItem(STORAGE_KEY, JSON.stringify({ bubbles: [{ kind: 'org', orgName: 'a', runId: 7, finished: 'yes' }, null, { kind: 'org' }] }))
    expect(loadState().bubbles.map(b => [b.key, b.finished])).toEqual([['org:a', false]])
  })

  it('a saved file with two live bubbles for one org keeps one live', () => {
    localStorage.setItem(STORAGE_KEY, JSON.stringify({ bubbles: [
      { kind: 'org', orgName: 'acme', runId: 'r1' }, { kind: 'org', orgName: 'acme', runId: 'r2' }, { kind: 'org', orgName: 'acme' },
    ] }))
    expect(loadState().bubbles.map(b => [b.key, b.finished])).toEqual([['org:acme#r1', false], ['org:acme#r2', true]])
    saveState({ bubbles: [], side: 'right' })
    expect(JSON.parse(localStorage.getItem(STORAGE_KEY)).bubbles).toEqual([])
  })
})

describe('a finished run bubble', () => {
  let store = null
  function Harness() {
    const s = useCoderBubbles()
    store = s
    useEffect(() => { s.openOrg('acme', { runId: 'r-old-12345', finished: true }) }, []) // eslint-disable-line react-hooks/exhaustive-deps
    return <CoderBubbles store={s} onNavigate={() => {}} />
  }

  it('reads that run, says it is finished and sends, answers and controls nothing', async () => {
    api.getOrgChatHistory.mockResolvedValue({ ...history, status: 'running' })
    render(<Harness />)
    await screen.findByTestId('org-bubble-overlay')
    await waitFor(() => expect(api.getOrgChatHistory).toHaveBeenCalledWith('acme', 'r-old-12345'))
    expect(api.getOrgLogs).toHaveBeenCalledWith('acme', 'r-old-12345')
    expect(screen.getByTestId('org-bubble-status')).toHaveAttribute('data-status', 'finished')
    expect(screen.getByTestId('org-bubble-run')).toHaveTextContent('r-old-12')
    // No Start/Pause/Stop/Resume even though the stored status string says running.
    for (const id of ['org-bubble-pause', 'org-bubble-resume', 'org-bubble-stop']) expect(screen.queryByTestId(id)).toBeNull()
    await waitFor(() => expect(screen.getByTestId('org-chat-thread')).not.toHaveAttribute('aria-busy', 'true'))
    expect(document.querySelector('[data-testid="org-bubble-overlay"] textarea')).toBeDisabled()
    // Pending asks are shown but cannot be acted on.
    expect(screen.queryByTestId('org-chat-stopped')).toBeNull()
    expect(screen.getAllByRole('button').filter(b => /^(Approve|Deny|Send|Answer)$/i.test(b.textContent.trim())).every(b => b.disabled)).toBe(true)
    expect(api.sendOrgChat).not.toHaveBeenCalled()
    expect(api.answerOrgChat).not.toHaveBeenCalled()
    expect(api.controlOrg).not.toHaveBeenCalled()
  })

  it('every mutating control in it is disabled or absent', async () => {
    const items = [
      ...history.items,
      { kind: 'question', id: 'q-block', ref: 'q-block', role: 'dev', text: 'May I drop the table?', pending: true, blocking: true },
      { ...sendEmail, state: 'pending', pending: true },
    ]
    api.getOrgChatHistory.mockResolvedValue({ ...history, status: 'running', items })
    render(<Harness />)
    await screen.findByTestId('org-bubble-overlay')
    await waitFor(() => expect(screen.getByTestId('org-chat-thread')).not.toHaveAttribute('aria-busy', 'true'))
    const overlay = screen.getByTestId('org-bubble-overlay')
    // These exist (so the check cannot pass by absence of the cards)...
    for (const id of ['org-chat-dismiss', 'ask-always-allow', 'ask-approve', 'ask-edit', 'org-chat-answer', 'org-chat-approve', 'org-chat-deny', 'ask-reject']) {
      expect(overlay.querySelectorAll(`[data-testid="${id}"]`).length, id).toBeGreaterThan(0)
      // ...and every one of them is disabled.
      for (const el of overlay.querySelectorAll(`[data-testid="${id}"]`)) expect(el, id).toBeDisabled()
    }
    for (const el of overlay.querySelectorAll('.ask-card input, .ask-card textarea')) expect(el).toBeDisabled()
    expect(overlay.querySelector('.ask-card [data-testid="ask-text-edit"]')).toBeNull()
    // The composer and its send button.
    expect(overlay.querySelector('textarea:not(.ask-card textarea)')).toBeDisabled()
    // (a disabled composer titles its send button "No backend selected…"; an enabled one "Send message")
    expect(overlay.querySelector('button[title="Send message"]')).toBeNull()
    expect(overlay.querySelector('button[title^="No backend"]')).toBeDisabled()
    // Nothing reached the CLI, not even by a click on a disabled control.
    for (const el of overlay.querySelectorAll('[data-testid="org-chat-dismiss"], [data-testid="ask-edit"], [data-testid="ask-approve"]')) fireEvent.click(el)
    expect(api.sendOrgChat).not.toHaveBeenCalled()
    expect(api.answerOrgChat).not.toHaveBeenCalled()
    expect(api.resolveOrgChat).not.toHaveBeenCalled()
    expect(api.controlOrg).not.toHaveBeenCalled()
  })

  it('ignores the live bus: the log is a record', async () => {
    render(<Harness />)
    await screen.findByTestId('org-bubble-overlay')
    const before = api.getOrgChatHistory.mock.calls.length
    act(() => { for (const cb of listeners.org) cb({ orgName: 'acme', event: { id: 'x', ts: 9, org: 'acme', run: 'r-new', type: 'chat', from: 'ceo', to: 'human', msg: 'hello' } }) })
    await new Promise(r => setTimeout(r, 900))
    expect(api.getOrgChatHistory.mock.calls.length).toBe(before)
    expect(store.bubbles).toHaveLength(1)
  })

  it('Open in Orgs from the header carries the run', async () => {
    const onNavigate = vi.fn()
    function H() {
      const s = useCoderBubbles()
      useEffect(() => { s.openOrg('acme', { runId: 'r-old-12345', finished: true }) }, []) // eslint-disable-line react-hooks/exhaustive-deps
      return <CoderBubbles store={s} onNavigate={onNavigate} />
    }
    render(<H />)
    await screen.findByTestId('org-bubble-overlay')
    fireEvent.click(screen.getByRole('button', { name: /orgs/i }))
    expect(onNavigate).toHaveBeenCalledWith('orgs', { org: 'acme', run: 'r-old-12345' })
  })
})
