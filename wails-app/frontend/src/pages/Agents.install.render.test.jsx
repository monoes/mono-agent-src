// @vitest-environment jsdom
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

beforeEach(() => {
  vi.clearAllMocks()
  localStorage.clear()
})
afterEach(cleanup)

const tile = id => document.querySelector(`[data-runtime="${id}"]`)

describe('Agents page install actions', () => {
  it('offers Install, Update and Copy steps by recipe', async () => {
    render(<Agents onOpenChat={() => {}} />)
    await waitFor(() => expect(tile('codex')).toBeTruthy())
    expect(within(tile('codex')).getByText('Install')).toBeInTheDocument()
    expect(within(tile('claude')).getByText('Update')).toBeInTheDocument()
    expect(within(tile('claude')).getByText('sign in: claude login')).toBeInTheDocument()
    expect(within(tile('grok')).getByText('Copy steps')).toBeInTheDocument()
    expect(within(tile('grok')).queryByText('Install')).toBeNull()
  })

  it('installs only after the user confirms, then shows the result', async () => {
    mockConfirm.mockResolvedValue(true)
    mockInstall.mockImplementation(async (id, update, onLine) => { onLine('added 12 packages'); return { ok: true, message: 'codex 1.0 installed' } })
    render(<Agents onOpenChat={() => {}} />)
    await waitFor(() => expect(tile('codex')).toBeTruthy())
    fireEvent.click(within(tile('codex')).getByText('Install'))
    await waitFor(() => expect(mockInstall).toHaveBeenCalledWith('codex', false, expect.any(Function), ''))
    expect(mockConfirm).toHaveBeenCalledTimes(1)
    await screen.findByText('codex 1.0 installed')
  })

  it('does nothing when the confirmation is declined', async () => {
    mockConfirm.mockResolvedValue(false)
    render(<Agents onOpenChat={() => {}} />)
    await waitFor(() => expect(tile('claude')).toBeTruthy())
    fireEvent.click(within(tile('claude')).getByText('Update'))
    await waitFor(() => expect(mockConfirm).toHaveBeenCalled())
    expect(mockInstall).not.toHaveBeenCalled()
  })

  // Consent is for what runs: the dialog shows the script's real URL (not
  // the hint), and only that URL is approved to the CLI.
  it('shows and approves the exact installer URL for a script runtime', async () => {
    mockConfirm.mockResolvedValue(true)
    mockInstall.mockResolvedValue({ ok: true, message: 'hermes installed' })
    render(<Agents onOpenChat={() => {}} />)
    await waitFor(() => expect(tile('hermes')).toBeTruthy())
    fireEvent.click(within(tile('hermes')).getByText('Install'))
    await waitFor(() => expect(mockConfirm).toHaveBeenCalledTimes(1))
    render(mockConfirm.mock.calls[0][0])
    expect(screen.getByText('curl -fsSL https://other-host.example/install.sh | bash')).toBeInTheDocument()
    await waitFor(() => expect(mockInstall).toHaveBeenCalledWith('hermes', false, expect.any(Function), 'https://other-host.example/install.sh'))
  })

  // #146 item 9: with no recipe, a `curl … | bash` hint is the vendor
  // script (and only its URL is approved); a prose hint is copied, never
  // offered as an Install the CLI would refuse.
  it('handles an older monomind without a recipe like the CLI does', async () => {
    mockConfirm.mockResolvedValue(true)
    mockInstall.mockResolvedValue({ ok: true, message: 'kimi installed' })
    render(<Agents onOpenChat={() => {}} />)
    await waitFor(() => expect(tile('kimi')).toBeTruthy())
    expect(within(tile('droid')).getByText('Copy steps')).toBeInTheDocument()
    expect(within(tile('droid')).queryByText('Install')).toBeNull()
    fireEvent.click(within(tile('kimi')).getByText('Install'))
    await waitFor(() => expect(mockConfirm).toHaveBeenCalledTimes(1))
    render(mockConfirm.mock.calls[0][0])
    expect(screen.getByText(/downloads and runs the vendor’s install script/)).toBeInTheDocument()
    expect(screen.queryByText('This runs:')).toBeNull()
    await waitFor(() => expect(mockInstall).toHaveBeenCalledWith('kimi', false, expect.any(Function), 'https://kimi.example/install.sh'))
  })

  // #146 item 7: a failure is shown in full in a live region, and the
  // sign-in hint stays after a successful update.
  it('shows a failed install in full in a live region, and keeps the sign-in hint', async () => {
    mockConfirm.mockResolvedValue(true)
    const longError = 'npm ERR! code EACCES\nnpm ERR! syscall mkdir\nnpm ERR! path /usr/lib/node_modules/@openai — permission denied, try the managed Node'
    mockInstall.mockImplementation(async id => (id === 'codex' ? { ok: false, message: longError } : { ok: true, message: 'claude updated' }))
    render(<Agents onOpenChat={() => {}} />)
    await waitFor(() => expect(tile('codex')).toBeTruthy())
    fireEvent.click(within(tile('codex')).getByText('Install'))
    const region = screen.getByTestId('install-result-codex')
    expect(region).toHaveAttribute('aria-live', 'polite')
    await waitFor(() => expect(region).toHaveTextContent('permission denied, try the managed Node'))
    expect(region).toHaveTextContent('Install failed')

    fireEvent.click(within(tile('claude')).getByText('Update'))
    await within(tile('claude')).findByText('claude updated')
    expect(within(tile('claude')).getByText('sign in: claude login')).toBeInTheDocument()
  })

  // #146 item 8: the same runtime being installed from System health.
  it('says so when the runtime is already being installed elsewhere', async () => {
    mockConfirm.mockResolvedValue(true)
    mockInstall.mockResolvedValue({ ok: false, busy: true, message: 'already running' })
    render(<Agents onOpenChat={() => {}} />)
    await waitFor(() => expect(tile('codex')).toBeTruthy())
    fireEvent.click(within(tile('codex')).getByText('Install'))
    await within(tile('codex')).findByText(/codex is already being installed/)
  })
})
