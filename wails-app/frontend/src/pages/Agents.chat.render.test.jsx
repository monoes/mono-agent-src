// @vitest-environment jsdom
// Chat on a runtime tile opens a new chat bubble with that runtime picked.
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup, within } from '@testing-library/react'
import '../i18n.js'

const scan = {
  v: 1,
  agents: [
    { id: 'claude', installed: true, version: '2.1.0', install_hint: 'npm install -g @anthropic-ai/claude-code',
      install: { kind: 'npm', packages: ['@anthropic-ai/claude-code'] }, login_hint: 'claude login' },
    { id: 'codex', installed: false, install_hint: 'npm install -g @openai/codex',
      install: { kind: 'npm', packages: ['@openai/codex'] } },
    { id: 'grok', installed: false, install_hint: 'install the Grok Build CLI per https://docs.x.ai/build/cli',
      install: { kind: 'manual' } },
    // The hint and the recipe differ: what runs is the recipe.
    { id: 'hermes', installed: false, install_hint: 'see https://hermes.example/docs to install',
      install: { kind: 'script', url: 'https://other-host.example/install.sh', shell: 'bash' } },
    // Older monomind (no structured recipe): the CLI parses the hint.
    { id: 'kimi', installed: false, install_hint: 'curl -fsSL https://kimi.example/install.sh | bash' },
    { id: 'droid', installed: false, install_hint: 'download it from https://droid.example and follow the steps' },
  ],
}
const mockInstall = vi.fn()
const mockConfirm = vi.fn()
vi.mock('../lib/agentRuntimes.js', async importOriginal => ({
  ...(await importOriginal()),
  cachedAgentScan: () => Promise.resolve(scan),
  invalidateAgentScan: vi.fn(),
}))
vi.mock('../lib/health.js', () => ({
  installRuntime: (...a) => mockInstall(...a),
  runHealth: vi.fn(),
}))
vi.mock('../services/api.js', () => ({ api: { isMonomindInitialized: () => Promise.resolve(true) }, onMonomindInitEvent: () => () => {} }))
vi.mock('../components/ConfirmDialog.jsx', () => ({ confirm: (...a) => mockConfirm(...a) }))
vi.mock('../components/MonomindInitPrompt.jsx', () => ({ default: () => null }))
vi.mock('../components/agents/AgentRoster.jsx', () => ({ default: () => null }))

import Agents from './Agents.jsx'
import { CoderBubbleContext } from '../components/bubbles/CoderBubbleContext.jsx'

beforeEach(() => { vi.clearAllMocks() })
afterEach(cleanup)

describe('Agents page chat', () => {
  it('opens a bubble draft seeded with the runtime, not the assistant panel', async () => {
    const openNewChat = vi.fn()
    render(<CoderBubbleContext.Provider value={{ openDraft: null, openNewChat, navigate: null }}><Agents /></CoderBubbleContext.Provider>)
    await waitFor(() => expect(document.querySelector('[data-runtime="claude"]')).toBeTruthy())
    fireEvent.click(within(document.querySelector('[data-runtime="claude"]')).getByRole('button', { name: /chat/i }))
    expect(openNewChat).toHaveBeenCalledWith({ setup: { runtime: 'claude' } })
  })

  it('does not throw outside the app, where nothing can open a bubble', async () => {
    render(<Agents />)
    await waitFor(() => expect(document.querySelector('[data-runtime="claude"]')).toBeTruthy())
    expect(() => fireEvent.click(within(document.querySelector('[data-runtime="claude"]')).getByRole('button', { name: /chat/i }))).not.toThrow()
  })
})
