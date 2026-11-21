// @vitest-environment jsdom
// Your agents: a tile per scanned runtime, the empty state, the one-message
// test (one runtime, one model), Continue only once the roster holds a passed
// model, Check again, the way back when monomind is missing, and installs.
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, cleanup, act, within, waitFor } from '@testing-library/react'
import i18n from '../../i18n.js'

const { go, events } = vi.hoisted(() => ({
  go: {
    ScanAgentRuntimes: vi.fn(),
    GetAgentRuntimeModels: vi.fn(),
    InstallAgentRuntime: vi.fn(),
    StartAgentValidation: vi.fn(),
    AgentRoster: vi.fn(),
    RunHealthCheck: vi.fn(),
    RunHealthFix: vi.fn(),
    CancelHealthRun: vi.fn(),
  },
  events: {},
}))
vi.mock('../../wailsjs/go/main/App', () => go)
vi.mock('../../services/api.js', () => {
  const subscribeEvent = (name, cb) => {
    events[name] = cb
    return () => { if (events[name] === cb) delete events[name] }
  }
  return {
    api: {},
    subscribeEvent,
    onAgentValidate: cb => subscribeEvent('agents:validate', cb),
    onAgentValidateClosed: cb => subscribeEvent('agents:validateClosed', cb),
  }
})

import Agents from './Agents.jsx'

const emit = (name, payload) => act(() => { events[name]?.(payload) })

const claude = { id: 'claude', installed: true, binary: '/usr/local/bin/claude', version: '2.1.0', install_hint: 'npm install -g @anthropic-ai/claude-code', login_hint: 'claude /login', execution_supported: true }
const codex = { id: 'codex', installed: true, binary: '/usr/local/bin/codex', version: '0.50', install_hint: 'npm install -g @openai/codex', login_hint: 'codex login' }
const opencode = { id: 'opencode', installed: false, binary: null, version: null, install_hint: 'npm install -g opencode-ai', install: { kind: 'npm', packages: ['opencode-ai'] } }
const cursor = { id: 'cursor', installed: false, binary: null, version: null, install_hint: 'see https://cursor.com/cli', install: { kind: 'manual' } }
const scanOf = (...agents) => JSON.stringify({ v: 1, agents })
const rosterOf = (...runtimes) => JSON.stringify({ v: 1, runtimes })
const NO_ROSTER = rosterOf()
const READY_ROSTER = rosterOf({ runtime: 'claude', installed: true, ready: 1, models: [{ runtime: 'claude', model: 'sonnet', state: 'ready', status: 'ok' }] })

let props
beforeEach(async () => {
  await i18n.changeLanguage('en')
  vi.clearAllMocks()
  for (const k of Object.keys(events)) delete events[k]
  go.ScanAgentRuntimes.mockResolvedValue(scanOf(claude, codex, opencode, cursor))
  go.GetAgentRuntimeModels.mockImplementation((id) => Promise.resolve(JSON.stringify(
    id === 'claude'
      ? [{ id: 'sonnet', label: 'Sonnet' }, { id: 'opus', label: 'Opus' }, { id: 'claude-sonnet', label: 'Sonnet', alias_of: 'sonnet' }]
      : [{ id: 'gpt-5', label: 'GPT-5' }],
  )))
  go.AgentRoster.mockResolvedValue(NO_ROSTER)
  go.StartAgentValidation.mockResolvedValue('{"ok":true}')
  go.InstallAgentRuntime.mockResolvedValue('{"ok":true}')
  props = {
    state: { profileId: 'work', steps: [] },
    profileId: 'work',
    mode: 'full',
    reload: vi.fn(() => Promise.resolve({ ok: true })),
    setCanContinue: vi.fn(),
    setOnContinue: vi.fn(),
    setContinueLabel: vi.fn(),
    goTo: vi.fn(),
  }
})
afterEach(cleanup)

const lastCanContinue = () => props.setCanContinue.mock.calls.at(-1)?.[0]
const tile = (id) => document.querySelector(`[data-runtime="${id}"]`)
const findTile = (id) => waitFor(() => { const el = tile(id); if (!el) throw new Error(`no tile ${id}`); return el })
const tested = () => screen.findByRole('button', { name: 'Send a test message to Claude Code' })

describe('Agents step', () => {
  it('shows a tile per scanned runtime with its state', async () => {
    render(<Agents {...props} />)
    expect(screen.getByText('Looking for AI coding tools on this machine')).toBeInTheDocument()
    await waitFor(() => expect(tile('cursor')).toBeInTheDocument())
    expect(within(tile('claude')).getByText('Claude Code')).toBeInTheDocument()
    expect(within(tile('claude')).getByText('Installed')).toBeInTheDocument()
    expect(within(tile('codex')).getByText('Codex')).toBeInTheDocument()
    expect(within(tile('opencode')).getByText('Not installed')).toBeInTheDocument()
    expect(within(tile('opencode')).getByRole('button', { name: 'Install OpenCode' })).toBeInTheDocument()
    expect(within(tile('opencode')).getByText('npm install -g opencode-ai')).toBeInTheDocument()
    // a manual install shows the scan's own hint, never a button
    expect(within(tile('cursor')).getByText('see https://cursor.com/cli')).toBeInTheDocument()
    expect(within(tile('cursor')).queryByRole('button')).not.toBeInTheDocument()
    expect(screen.getByText("You'll find this later in Settings, under AI agents.")).toBeInTheDocument()
    // models are read with the scan's binary; aliases are left out
    expect(go.GetAgentRuntimeModels).toHaveBeenCalledWith('claude', '/usr/local/bin/claude')
    const picker = await within(tile('claude')).findByRole('combobox', { name: 'Model for Claude Code' })
    expect(within(picker).getAllByRole('option').map(o => o.textContent)).toEqual(['Sonnet', 'Opus'])
    expect(picker).toHaveValue('sonnet')
    expect(screen.getByText('This sends one short test message to Claude Code.')).toBeInTheDocument()
    expect(lastCanContinue()).toBe(false)
  })

  it('shows the empty state with a recommended install when none is installed', async () => {
    go.ScanAgentRuntimes.mockResolvedValue(scanOf({ ...claude, installed: false, binary: null, install: { kind: 'npm', packages: ['@anthropic-ai/claude-code'] } }, opencode))
    render(<Agents {...props} />)
    expect(await screen.findByText('No agents found. Install one to continue.')).toBeInTheDocument()
    expect(screen.getByText('Recommended: Claude Code.')).toBeInTheDocument()
    expect(within(tile('claude')).getByRole('button', { name: 'Install Claude Code' })).toBeInTheDocument()
    expect(go.GetAgentRuntimeModels).not.toHaveBeenCalled()
    expect(lastCanContinue()).toBe(false)
  })

  it('validation is started with one runtime and one model', async () => {
    render(<Agents {...props} />)
    const picker = await within(await findTile('claude')).findByRole('combobox', { name: 'Model for Claude Code' })
    fireEvent.change(picker, { target: { value: 'opus' } })
    fireEvent.click(await tested())
    await waitFor(() => expect(go.StartAgentValidation).toHaveBeenCalledTimes(1))
    expect(go.StartAgentValidation).toHaveBeenCalledWith(['claude'], ['opus'], false)
    expect(await within(tile('claude')).findByText('Sending a test message…')).toBeInTheDocument()
  })

  it('enables Continue only after the roster shows a passed entry', async () => {
    render(<Agents {...props} />)
    fireEvent.click(await tested())
    await waitFor(() => expect(go.StartAgentValidation).toHaveBeenCalled())
    expect(lastCanContinue()).toBe(false)
    go.AgentRoster.mockResolvedValue(READY_ROSTER)
    emit('agents:validate', { type: 'validate.result', result: { runtime: 'claude', model: 'sonnet', status: 'ok', reply: 'pong' } })
    expect(lastCanContinue()).toBe(false) // the live result alone does not count
    emit('agents:validateClosed', { ok: true })
    await waitFor(() => expect(lastCanContinue()).toBe(true))
    expect(within(tile('claude')).getByText('Ready')).toBeInTheDocument()
    expect(props.reload).toHaveBeenCalled()
  })

  it('a test that ends after the person left and came back still enables Continue', async () => {
    const first = render(<Agents {...props} />)
    fireEvent.click(await tested())
    await waitFor(() => expect(go.StartAgentValidation).toHaveBeenCalled())
    first.unmount()
    render(<Agents {...props} />)
    await findTile('claude')
    expect(lastCanContinue()).toBe(false)
    go.AgentRoster.mockResolvedValue(READY_ROSTER)
    emit('agents:validateClosed', { ok: true })
    await waitFor(() => expect(lastCanContinue()).toBe(true))
    expect(props.reload).toHaveBeenCalled()
  })

  it('starts with Continue enabled when the roster already holds a passed entry', async () => {
    go.AgentRoster.mockResolvedValue(READY_ROSTER)
    render(<Agents {...props} />)
    await waitFor(() => expect(lastCanContinue()).toBe(true))
  })

  it('Check again rescans', async () => {
    render(<Agents {...props} />)
    await waitFor(() => expect(tile('claude')).toBeInTheDocument())
    expect(go.ScanAgentRuntimes).toHaveBeenCalledTimes(1)
    go.ScanAgentRuntimes.mockResolvedValue(scanOf(claude, codex, { ...opencode, installed: true, binary: '/bin/opencode' }, cursor))
    fireEvent.click(screen.getByRole('button', { name: 'Check again' }))
    await waitFor(() => expect(go.ScanAgentRuntimes).toHaveBeenCalledTimes(2))
    expect(await within(tile('opencode')).findByText('Installed')).toBeInTheDocument()
    expect(go.AgentRoster).toHaveBeenCalledTimes(2)
  })

  it('monomind missing shows the way back to the system check', async () => {
    go.ScanAgentRuntimes.mockResolvedValue(JSON.stringify({ error: 'monomind not found: install it with npm install -g @monoes/monomindcli' }))
    render(<Agents {...props} />)
    const back = await screen.findByRole('button', { name: 'Back to system check' })
    expect(screen.getByText('monomind is not installed yet. The system check installs it.')).toBeInTheDocument()
    fireEvent.click(back)
    expect(props.goTo).toHaveBeenCalledWith('system')
    expect(lastCanContinue()).toBe(false)
  })

  it('a scan refused for sign-in shows the sign-in message', async () => {
    go.ScanAgentRuntimes.mockResolvedValue(JSON.stringify({ error: 'Log in to monoes.me first: monoagentcli account login', login_required: true }))
    render(<Agents {...props} />)
    expect(await screen.findByRole('alert')).toHaveTextContent('Your sign-in has ended. Sign in to monoes.me to continue.')
    expect(screen.getByRole('button', { name: 'Check again' })).toBeEnabled()
  })

  it('a failed test message shows the reason and leaves Continue disabled', async () => {
    render(<Agents {...props} />)
    fireEvent.click(await tested())
    await waitFor(() => expect(go.StartAgentValidation).toHaveBeenCalled())
    go.AgentRoster.mockResolvedValue(rosterOf({ runtime: 'claude', installed: true, ready: 0, login_hint: 'claude /login', models: [{ runtime: 'claude', model: 'sonnet', state: 'failed', status: 'auth', detail: 'Not logged in', login_hint: 'claude /login' }] }))
    emit('agents:validate', { type: 'validate.result', result: { runtime: 'claude', model: 'sonnet', status: 'auth', detail: 'Not logged in', login_hint: 'claude /login' } })
    emit('agents:validateClosed', { ok: true })
    expect(await within(tile('claude')).findByRole('alert')).toHaveTextContent('The test message failed: Not logged in')
    expect(await within(tile('claude')).findByText('Sign in needed')).toBeInTheDocument()
    expect(within(tile('claude')).getByText('claude /login')).toBeInTheDocument()
    expect(await tested()).toBeEnabled()
    expect(lastCanContinue()).toBe(false)
    expect(props.reload).not.toHaveBeenCalled()
  })

  it('a test refused at once shows its error and re-enables the button', async () => {
    go.StartAgentValidation.mockResolvedValue(JSON.stringify({ error: 'monoagentcli not found' }))
    render(<Agents {...props} />)
    fireEvent.click(await tested())
    expect(await within(tile('claude')).findByRole('alert')).toHaveTextContent('The test message failed: monoagentcli not found')
    expect(await tested()).toBeEnabled()
  })

  it('installs an npm runtime with its progress lines, then rescans', async () => {
    render(<Agents {...props} />)
    const install = await within(await findTile('opencode')).findByRole('button', { name: 'Install OpenCode' })
    fireEvent.click(install)
    await waitFor(() => expect(go.InstallAgentRuntime).toHaveBeenCalledWith('opencode', false, ''))
    emit('health:fixProgress', { fix_id: 'agent.install:opencode', kind: 'line', message: 'added 12 packages' })
    expect(within(tile('opencode')).getByText('added 12 packages')).toBeInTheDocument()
    go.ScanAgentRuntimes.mockResolvedValue(scanOf(claude, codex, { ...opencode, installed: true, binary: '/bin/opencode' }, cursor))
    emit('health:fixProgress', { fix_id: 'agent.install:opencode', kind: 'done', message: '' })
    await waitFor(() => expect(go.ScanAgentRuntimes).toHaveBeenCalledTimes(2))
    expect(await within(tile('opencode')).findByText('Installed')).toBeInTheDocument()
  })

  it('a failed install shows its reason and offers Install again', async () => {
    render(<Agents {...props} />)
    fireEvent.click(await within(await findTile('opencode')).findByRole('button', { name: 'Install OpenCode' }))
    await waitFor(() => expect(go.InstallAgentRuntime).toHaveBeenCalled())
    emit('health:fixProgress', { fix_id: 'agent.install:opencode', kind: 'error', message: 'npm ERR! EACCES' })
    expect(await within(tile('opencode')).findByRole('alert')).toHaveTextContent('Installing failed: npm ERR! EACCES')
    expect(within(tile('opencode')).getByRole('button', { name: 'Install OpenCode' })).toBeEnabled()
  })
})
