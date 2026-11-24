// @vitest-environment jsdom
// W4 r20-17: a failed refresh keeps the cached agent list and says so.
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, waitFor, cleanup } from '@testing-library/react'
import '../i18n.js'

vi.mock('../lib/agentRuntimes.js', async importOriginal => ({
  ...(await importOriginal()),
  cachedAgentScan: () => Promise.resolve({ error: 'monomind exploded' }),
  invalidateAgentScan: vi.fn(),
}))
vi.mock('../lib/health.js', () => ({ installRuntime: vi.fn(), runHealth: vi.fn() }))
vi.mock('../services/api.js', () => ({ api: { isMonomindInitialized: () => Promise.resolve(true) } }))
vi.mock('../components/ConfirmDialog.jsx', () => ({ confirm: vi.fn() }))
vi.mock('../components/MonomindInitPrompt.jsx', () => ({ default: () => null }))
vi.mock('../components/agents/AgentRoster.jsx', () => ({ default: () => null }))
import Agents from './Agents.jsx'

beforeEach(() => {
  localStorage.clear()
  localStorage.setItem('monoagent:agentScanCache:v1', JSON.stringify({
    cachedAt: Date.now(),
    res: { v: 1, agents: [{ id: 'claude', installed: true, version: '2.1.0' }] },
  }))
})
afterEach(cleanup)

describe('Agents refresh failure', () => {
  it('keeps the cached list, shows a notice, and does not overwrite the good cache', async () => {
    render(<Agents />)
    await screen.findByTestId('agents-refresh-notice')
    expect(document.querySelector('[data-runtime="claude"]')).toBeTruthy()
    expect(screen.getByTestId('agents-refresh-notice')).toHaveTextContent('monomind exploded')
    await waitFor(() => expect(JSON.parse(localStorage.getItem('monoagent:agentScanCache:v1')).res.error).toBeUndefined())
  })
})
