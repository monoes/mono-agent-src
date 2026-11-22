// @vitest-environment jsdom
// The AI agents page and monomind: the per-profile set-up prompt (its init
// answers, a finished init from any page) and the "monomind missing" state.
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
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
