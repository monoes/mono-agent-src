// @vitest-environment jsdom
// W5 r30-4: with no coder bubble provider, Chat on a runtime says it cannot open instead of staying silent.
import React from 'react'
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup, within } from '@testing-library/react'
import '../i18n.js'

const scan = { v: 1, agents: [{ id: 'claude', installed: true, version: '2.1.0', install: { kind: 'npm', packages: ['x'] } }] }
vi.mock('../lib/agentRuntimes.js', async importOriginal => ({
  ...(await importOriginal()),
  cachedAgentScan: () => Promise.resolve(scan),
  invalidateAgentScan: vi.fn(),
}))
vi.mock('../lib/health.js', () => ({ installRuntime: vi.fn(), runHealth: vi.fn() }))
vi.mock('../services/api.js', () => ({ api: { isMonomindInitialized: () => Promise.resolve(true) } }))
vi.mock('../components/ConfirmDialog.jsx', () => ({ confirm: vi.fn() }))
vi.mock('../components/MonomindInitPrompt.jsx', () => ({ default: () => null }))
vi.mock('../components/agents/AgentRoster.jsx', () => ({ default: () => null }))

import Agents from './Agents.jsx'

afterEach(cleanup)

describe('Agents page chat without a bubble provider', () => {
  it('shows a visible note on click', async () => {
    render(<Agents />)
    await waitFor(() => expect(document.querySelector('[data-runtime="claude"]')).toBeTruthy())
    fireEvent.click(within(document.querySelector('[data-runtime="claude"]')).getByRole('button', { name: /chat/i }))
    expect(await screen.findByText(/could not open from here/i)).toBeInTheDocument()
  })
})
