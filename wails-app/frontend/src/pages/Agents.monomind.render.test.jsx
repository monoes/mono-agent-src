// @vitest-environment jsdom
// The AI agents page and monomind: the per-profile set-up prompt (its init
// answers, a finished init from any page) and the "monomind missing" state.
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup, within, act } from '@testing-library/react'
import '../i18n.js'

const { go, api, events, notify, scanRef } = vi.hoisted(() => ({
  go: { InitializeMonomindProfile: vi.fn() },
  api: { isMonomindInitialized: vi.fn() },
  events: {},
  notify: vi.fn(),
  scanRef: { current: null },
}))
vi.mock('../wailsjs/go/main/App', () => go)
vi.mock('../services/api.js', () => ({
  api,
  notify: (...a) => notify(...a),
  onMonomindInitEvent: (cb) => {
    (events.init ||= new Set()).add(cb)
    return () => events.init?.delete(cb)
  },
}))
vi.mock('../lib/agentRuntimes.js', async importOriginal => ({
  ...(await importOriginal()),
  cachedAgentScan: () => Promise.resolve(scanRef.current),
  invalidateAgentScan: vi.fn(),
}))
vi.mock('../lib/health.js', () => ({ installRuntime: vi.fn(), runHealth: vi.fn(), runFix: vi.fn() }))
vi.mock('../components/ConfirmDialog.jsx', () => ({ confirm: vi.fn(() => Promise.resolve(false)) }))
vi.mock('../components/agents/AgentRoster.jsx', () => ({ default: () => null }))
vi.mock('../components/agents/OpenRunBoss.jsx', () => ({ default: () => null }))

import Agents from './Agents.jsx'

const SCAN = { v: 1, agents: [{ id: 'claude', installed: true, version: '2.1.0', install_hint: 'npm install -g @anthropic-ai/claude-code' }] }
const setUp = () => screen.findByRole('button', { name: 'Set up agents for this profile' })
const emit = (payload) => act(() => { for (const cb of [...(events.init || [])]) cb(payload) })

beforeEach(() => {
  vi.clearAllMocks()
  localStorage.clear()
  for (const k of Object.keys(events)) delete events[k]
  scanRef.current = SCAN
  api.isMonomindInitialized.mockResolvedValue(false)
  go.InitializeMonomindProfile.mockResolvedValue('{"ok":true}')
})
afterEach(cleanup)

describe('Agents page and monomind', () => {
  it('monomind missing offers Fix monomind', async () => {
    scanRef.current = { error: 'monomind not found (AI engine) — install it with npm install -g @monoes/monomindcli' }
    render(<Agents />)
    expect(await screen.findByText(/monomind not found \(AI engine\)/)).toBeInTheDocument()
    const fix = screen.getByTestId('fix-monomind')
    expect(fix).toBeInTheDocument()
    expect(within(fix).getByRole('button', { name: /Fix monomind/i })).toBeEnabled()
  })

  it('the set-up prompt goes away when a set-up started elsewhere finishes', async () => {
    render(<Agents />)
    expect(await setUp()).toBeInTheDocument()
    api.isMonomindInitialized.mockResolvedValue(true)
    // the Documents page or the onboarding ran it; the prompt here did not
    emit({ kind: 'done', message: '' })
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Set up agents for this profile' })).not.toBeInTheDocument())
  })

  it('reads the set-up state again when the page is shown again', async () => {
    api.isMonomindInitialized.mockResolvedValue(true)
    const view = render(<Agents isActive />)
    await waitFor(() => expect(api.isMonomindInitialized).toHaveBeenCalledTimes(1))
    view.rerender(<Agents isActive={false} />)
    api.isMonomindInitialized.mockResolvedValue(false)
    view.rerender(<Agents isActive />)
    expect(await setUp()).toBeInTheDocument()
    expect(api.isMonomindInitialized).toHaveBeenCalledTimes(2)
  })

  it('a remount after a profile switch reads the new profile', async () => {
    api.isMonomindInitialized.mockResolvedValue(true)
    const first = render(<Agents />)
    await waitFor(() => expect(api.isMonomindInitialized).toHaveBeenCalledTimes(1))
    expect(screen.queryByRole('button', { name: 'Set up agents for this profile' })).not.toBeInTheDocument()
    first.unmount()
    // every switch reloads the window (Sidebar.jsx handleSwitchProfile), so the page mounts again
    api.isMonomindInitialized.mockResolvedValue(false)
    render(<Agents />)
    expect(await setUp()).toBeInTheDocument()
  })

  it('a sign-in refusal of the set-up stops the spinner and says to sign in', async () => {
    go.InitializeMonomindProfile.mockResolvedValue(JSON.stringify({ error: 'Log in to monoes.me first: monoagentcli account login', login_required: true }))
    render(<Agents />)
    fireEvent.click(await setUp())
    await waitFor(() => expect(notify).toHaveBeenCalledWith('initialize monomind', expect.stringMatching(/sign in to monoes\.me, then try again/)))
    expect(await setUp()).toBeInTheDocument()
    expect(document.querySelector('.spinner')).toBeNull()
  })

  it('an error answer of the set-up stops the spinner and reports it', async () => {
    go.InitializeMonomindProfile.mockRejectedValue(new Error('monoagentcli not found'))
    render(<Agents />)
    fireEvent.click(await setUp())
    await waitFor(() => expect(notify).toHaveBeenCalledWith('initialize monomind', 'monoagentcli not found'))
    expect(await setUp()).toBeInTheDocument()
    expect(document.querySelector('.spinner')).toBeNull()
  })
})
