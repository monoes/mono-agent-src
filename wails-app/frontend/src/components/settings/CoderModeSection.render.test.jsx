// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
import ConfirmHost from '../ConfirmDialog.jsx'
import CoderModeSection, { CODER_RISK_TITLE } from './CoderModeSection.jsx'

vi.mock('react-i18next', async () => {
  const en = (await import('../../locales/en.json')).default
  const t = (key, o = {}) => String(key.split('.').reduce((n, k) => n?.[k], en) ?? key).replace(/{{(\w+)}}/g, (_, k) => o[k])
  return { useTranslation: () => ({ t, i18n: { language: 'en' } }) }
})

const mockRunFix = vi.fn()
vi.mock('../../lib/health.js', () => ({ runFix: (...a) => mockRunFix(...a) }))

const App = {}
beforeEach(() => {
  mockRunFix.mockReset()
  for (const k of ['CoderStatus', 'CoderEnable', 'CoderDisable', 'CoderSet', 'PickCoderFolder']) App[k] = vi.fn()
  window.go = { main: { App } }
})
afterEach(() => { cleanup(); delete window.go })

const status = (over = {}) => JSON.stringify({
  enabled: false, workspaceRoot: '/home/u/monoagent-coder', maxTurns: 200, timeout: '60m', budgetUsd: 0,
  ready: true, missingCapabilities: [], monomindVersion: '1.2.3', runtime: 'claude', ...over,
})

function mount(st) {
  App.CoderStatus.mockResolvedValue(st)
  return render(<><CoderModeSection /><ConfirmHost /></>)
}

describe('CoderModeSection', () => {
  it('is off by default and shows the saved settings', async () => {
    mount(status())
    const toggle = await screen.findByRole('checkbox', { name: 'Coder mode' })
    await waitFor(() => expect(toggle).not.toBeDisabled())
    expect(toggle).not.toBeChecked()
    expect(screen.getByLabelText('Coder root')).toHaveValue('/home/u/monoagent-coder')
    expect(screen.getByLabelText('Max turns')).toHaveValue('200')
    expect(screen.getByLabelText('Timeout')).toHaveValue('60m')
    expect(screen.getByLabelText('Budget per turn')).toHaveValue('')
  })

  it('turning it on asks for the risk confirmation first; cancelling leaves it off', async () => {
    mount(status())
    const toggle = await screen.findByRole('checkbox', { name: 'Coder mode' })
    await waitFor(() => expect(toggle).not.toBeDisabled())
    fireEvent.click(toggle)
    const dialog = await screen.findByRole('dialog', { name: CODER_RISK_TITLE })
    expect(dialog).toHaveTextContent('run any command, read and change any file your user account can, and install software')
    expect(dialog).toHaveTextContent('Only point it at folders and repos you trust')
    expect(dialog).toHaveTextContent('the coding agent you pick (Claude Code, Codex, OpenCode, …)')
    expect(dialog).toHaveTextContent('instructions files (CLAUDE.md, AGENTS.md, GEMINI.md), skills, hooks and MCP servers')
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(App.CoderEnable).not.toHaveBeenCalled()
    expect(toggle).not.toBeChecked()
  })

  it('confirming turns it on through the CLI', async () => {
    mount(status())
    App.CoderEnable.mockResolvedValue(status({ enabled: true }))
    const toggle = await screen.findByRole('checkbox', { name: 'Coder mode' })
    await waitFor(() => expect(toggle).not.toBeDisabled())
    fireEvent.click(toggle)
    fireEvent.click(await screen.findByRole('button', { name: 'Turn on Coder mode' }))
    await waitFor(() => expect(App.CoderEnable).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(toggle).toBeChecked())
  })

  it('turning it off needs no confirmation', async () => {
    mount(status({ enabled: true }))
    App.CoderDisable.mockResolvedValue(status({ enabled: false }))
    const toggle = await screen.findByRole('checkbox', { name: 'Coder mode' })
    await waitFor(() => expect(toggle).toBeChecked())
    fireEvent.click(toggle)
    await waitFor(() => expect(App.CoderDisable).toHaveBeenCalled())
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    await waitFor(() => expect(toggle).not.toBeChecked())
  })

  it('shows the monomind version when ready', async () => {
    mount(status({ enabled: true, monomindVersion: '2.17.0' }))
    expect(await screen.findByTestId('coder-ready')).toHaveTextContent('Ready · monomind 2.17.0 · claude')
    expect(screen.queryByTestId('coder-needs-update')).not.toBeInTheDocument()
  })

  it('lists each runtime\'s readiness, and binds the budget only where cost is reported', async () => {
    const rt = (id, over = {}) => ({ id, installed: true, fullAccess: true, ready: true, toolActivity: 'full', resume: true, effort: true, maxTurns: true, reportsCost: true, initTarget: id, ...over })
    mount(status({ enabled: true, monomindVersion: '2.19.0', runtimes: [
      rt('claude'), rt('codex', { toolActivity: 'start-only', maxTurns: false, reportsCost: false }), rt('kimicode', { installed: false, ready: false }),
    ] }))
    expect(await screen.findByTestId('coder-ready')).toHaveTextContent('Ready · monomind 2.19.0 · claude, codex')
    const list = screen.getByTestId('coder-runtimes')
    expect(list).toHaveTextContent('claude · ready')
    expect(list).toHaveTextContent('codex · ready · tool calls: start-only')
    expect(list).toHaveTextContent('kimicode · not installed')
    expect(screen.getByLabelText('Budget per turn')).toBeInTheDocument()
    expect(screen.getByTestId('coder-limits-hint')).toHaveTextContent('Max turns applies on claude; the budget on claude.')
    cleanup()

    mount(status({ enabled: true, runtimes: [rt('claude', { ready: false, installed: false }), rt('codex', { reportsCost: false, maxTurns: false })] }))
    await screen.findByTestId('coder-runtimes')
    expect(screen.queryByLabelText('Budget per turn')).not.toBeInTheDocument()
    expect(screen.getByTestId('coder-limits-hint')).toHaveTextContent('no ready runtime reports cost, so there is no budget')
  })

  it('with no runtime ready, says which part is missing', async () => {
    mount(status({ ready: false, runtimes: [{ id: 'claude', installed: false, fullAccess: true, ready: false }] }))
    expect(await screen.findByTestId('coder-needs-update')).toHaveTextContent('needs a coding runtime (claude, codex, opencode, …) installed')
    expect(screen.getByTestId('coder-needs-update')).toHaveTextContent('Coder chats start once a runtime is ready')
  })

  it('a monomind that could not be run shows the reason, not an update', async () => {
    mount(status({ ready: false, missingCapabilities: [], monomindVersion: '', probeError: 'monomind: command not found' }))
    const box = await screen.findByTestId('coder-needs-update')
    expect(box).toHaveTextContent("monomind couldn't be used: monomind: command not found")
    expect(box).not.toHaveTextContent('needs monomind update')
    expect(screen.getByRole('button', { name: 'Fix monomind' })).toBeInTheDocument()
  })

  it('Fix monomind confirms with the command, runs the fix, and reloads the status', async () => {
    mockRunFix.mockResolvedValue({ ok: true })
    mount(status({ ready: false, missingCapabilities: [], monomindVersion: '', probeError: 'shim not found' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Fix monomind' }))
    expect(await screen.findByRole('dialog')).toHaveTextContent('monoagentcli doctor fix monomind.install')
    App.CoderStatus.mockResolvedValue(status())
    fireEvent.click(screen.getByRole('button', { name: 'Run it' }))
    await waitFor(() => expect(mockRunFix).toHaveBeenCalledWith('monomind.install', expect.any(Function)))
    await waitFor(() => expect(App.CoderStatus).toHaveBeenCalledTimes(2))
  })

  it('says monomind needs an update when not ready, and still allows turning it on', async () => {
    mount(status({ ready: false, missingCapabilities: ['agent-exec-tool-activity', 'init-json'] }))
    expect(await screen.findByTestId('coder-needs-update')).toHaveTextContent('needs monomind update (found monomind 1.2.3, missing: agent-exec-tool-activity, init-json)')
    expect(screen.getByRole('checkbox', { name: 'Coder mode' })).not.toBeDisabled()
  })

  it('saves the workspace root (from the folder picker) and defaults with coder set', async () => {
    mount(status({ enabled: true }))
    App.PickCoderFolder.mockResolvedValue('/data/coder')
    App.CoderSet.mockResolvedValue(status({ enabled: true, workspaceRoot: '/data/coder', maxTurns: 50, timeout: '30m', budgetUsd: 1.5 }))
    await screen.findByLabelText('Coder root')
    fireEvent.click(screen.getByRole('button', { name: /Browse/ }))
    await waitFor(() => expect(screen.getByLabelText('Coder root')).toHaveValue('/data/coder'))
    fireEvent.change(screen.getByLabelText('Max turns'), { target: { value: '50' } })
    fireEvent.change(screen.getByLabelText('Timeout'), { target: { value: '30m' } })
    fireEvent.change(screen.getByLabelText('Budget per turn'), { target: { value: '1.5' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    // The org router (the coder-org settings' own) is left alone: ''.
    await waitFor(() => expect(App.CoderSet).toHaveBeenCalledWith('/data/coder', 50, '30m', 1.5, ''))
    expect(await screen.findByText(/Saved\./)).toBeInTheDocument()
  })

  it('shows the CLI error', async () => {
    mount(status({ enabled: true }))
    App.CoderSet.mockResolvedValue(JSON.stringify({ error: 'invalid --timeout "soon"', code: 'invalid_input' }))
    fireEvent.change(await screen.findByLabelText('Timeout'), { target: { value: 'soon' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('invalid --timeout "soon"')
  })
})
