// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, waitFor, cleanup } from '@testing-library/react'
import '../i18n.js'

// A live open-ended run's boss is shown on the Agents page whatever state the rest of the page is in: the profile not set up with
// monomind, the monomind scan failing, or no runtimes at all. Without a run the page is exactly what it was.
const h = vi.hoisted(() => ({ scan: { v: 1, agents: [] }, scanError: null, initialized: true, sessions: [] }))
vi.mock('../lib/agentRuntimes.js', async importOriginal => ({
  ...(await importOriginal()),
  cachedAgentScan: () => (h.scanError ? Promise.resolve({ error: h.scanError }) : Promise.resolve(h.scan)),
  invalidateAgentScan: vi.fn(),
}))
vi.mock('../lib/health.js', () => ({ installRuntime: vi.fn(), runHealth: vi.fn() }))
vi.mock('../services/api.js', () => ({ api: { isMonomindInitialized: () => Promise.resolve(h.initialized) } }))
vi.mock('../services/taskSessions.js', () => ({ sessionsApi: { list: () => Promise.resolve({ sessions: h.sessions }) } }))
vi.mock('../components/MonomindInitPrompt.jsx', () => ({ default: () => <div data-testid="init-prompt" /> }))
vi.mock('../components/agents/AgentRoster.jsx', () => ({ default: () => null }))

import Agents from './Agents.jsx'

const BOSS = { id: 7, boss: true, state: 'running', archived: false, spent_usd: 0.5 }
beforeEach(() => {
  localStorage.clear()
  Object.assign(h, { scan: { v: 1, agents: [{ id: 'claude', installed: true, version: '2' }] }, scanError: null, initialized: true, sessions: [] })
})
afterEach(cleanup)

describe('the open run\'s boss on the Agents page', () => {
  it('shows it when the profile is not set up with monomind, above the setup prompt', async () => {
    h.initialized = false; h.sessions = [BOSS]
    render(<Agents onOpenChat={() => {}} />)
    expect(await screen.findByTestId('open-run-boss')).toHaveTextContent('#7')
    await screen.findByTestId('init-prompt')
  })
  it('shows it when the monomind scan fails', async () => {
    h.scanError = 'monomind not found'; h.sessions = [BOSS]
    render(<Agents onOpenChat={() => {}} />)
    expect(await screen.findByTestId('open-run-boss')).toBeInTheDocument()
  })
  it('shows it when there are no runtimes at all', async () => {
    h.scan = { v: 1, agents: [] }; h.sessions = [BOSS]
    render(<Agents onOpenChat={() => {}} />)
    expect(await screen.findByTestId('open-run-boss')).toBeInTheDocument()
  })
  it('shows it beside the runtimes of a set-up profile', async () => {
    h.sessions = [BOSS]
    render(<Agents onOpenChat={() => {}} />)
    expect(await screen.findByTestId('open-run-boss')).toBeInTheDocument()
    expect(document.querySelector('[data-runtime="claude"]')).toBeTruthy()
  })
  it('shows nothing extra without a run: the setup prompt alone', async () => {
    h.initialized = false; h.sessions = []
    render(<Agents onOpenChat={() => {}} />)
    await screen.findByTestId('init-prompt')
    await waitFor(() => expect(screen.queryByTestId('open-run-boss')).toBeNull())
  })
  it('does not show a boss whose run is over (stopped or archived)', async () => {
    h.initialized = false; h.sessions = [{ ...BOSS, state: 'stopped' }, { ...BOSS, id: 8, archived: true }]
    render(<Agents onOpenChat={() => {}} />)
    await screen.findByTestId('init-prompt')
    expect(screen.queryByTestId('open-run-boss')).toBeNull()
  })
})
