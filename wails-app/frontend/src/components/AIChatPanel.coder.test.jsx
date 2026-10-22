// @vitest-environment jsdom
// Coder mode in the chat panel (#203): the Assistant | Coder choice before
// a conversation starts, the workspace choice, the mode locked once the
// chat has started, the coder header, the history badge, and Stop.
import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import '../i18n.js'
import { render, screen, cleanup, waitFor, fireEvent, within } from '@testing-library/react'

Element.prototype.scrollIntoView = Element.prototype.scrollIntoView || (() => {})
Element.prototype.scrollTo = Element.prototype.scrollTo || (() => {})

const m = vi.hoisted(() => ({
  listChatConversations: vi.fn(),
  createChatConversation: vi.fn(),
  createCoderConversation: vi.fn(),
  startChatTurn: vi.fn(),
  stopChatTurn: vi.fn(),
  getChatTurns: vi.fn(),
  coderStatus: vi.fn(),
  coderWorkspaceDefault: vi.fn(),
  coderWorkspaceList: vi.fn(),
  getActiveProfile: vi.fn(),
  pickCoderFolder: vi.fn(),
  openPathWithOS: vi.fn(),
}))

vi.mock('../services/api.js', async (importOriginal) => {
  const actual = await importOriginal()
  const api = {
    ...actual.api,
    scanAgentRuntimes: vi.fn().mockResolvedValue({ agents: [{ id: 'codex', installed: true, binary: '' }, { id: 'claude', installed: true, binary: '' }] }),
    getAgentRuntimeModels: vi.fn().mockResolvedValue([{ id: 'sonnet' }]),
    getChatEvents: vi.fn().mockResolvedValue({ items: [], hasMore: false }),
  }
  for (const k of Object.keys(m)) api[k] = (...args) => m[k](...args)
  return { ...actual, api, onChatEvent: () => () => {} }
})

import AIChatPanel from './AIChatPanel.jsx'
import { initSummary, keyInitFiles, CoderInitNote } from './chat/CoderHeader.jsx'

const status = (over = {}) => ({
  enabled: true, workspaceRoot: '/home/u/monoagent-coder', maxTurns: 200, timeout: '60m', budgetUsd: 0,
  ready: true, missingCapabilities: [], runtime: 'claude', ...over,
})
const WS = '/home/u/monoagent-coder'

beforeEach(() => {
  for (const f of Object.values(m)) f.mockReset()
  m.listChatConversations.mockResolvedValue({ items: [] })
  m.getChatTurns.mockResolvedValue({ items: [] })
  m.getActiveProfile.mockResolvedValue({ id: 'work', name: 'Work Stuff', root_dir: WS })
  m.coderWorkspaceList.mockResolvedValue([{ path: '/w/recent-proj', lastUsed: '2026-09-27T10:00:00Z', conversations: 2, exists: true }])
  m.startChatTurn.mockResolvedValue({ ok: true, status: 'active' })
  m.stopChatTurn.mockResolvedValue({ ok: true })
})
afterEach(() => { cleanup() })

async function openPanel(props = {}) {
  render(<AIChatPanel workflowID="general" isOpen={true} onClose={() => {}} {...props} />)
  await screen.findByPlaceholderText('Type a message...')
  await waitFor(() => expect(screen.getByRole('button', { name: 'Send message' }).title).not.toMatch(/No backend/))
}

async function sendMessage(text) {
  fireEvent.change(screen.getByPlaceholderText('Type a message...'), { target: { value: text } })
  const send = screen.getByRole('button', { name: 'Send message' })
  await waitFor(() => expect(send).not.toBeDisabled())
  fireEvent.click(send)
}

describe('AIChatPanel coder mode', () => {
  it('shows no mode choice while coder mode is off in Settings', async () => {
    m.coderStatus.mockResolvedValue(status({ enabled: false }))
    await openPanel()
    await waitFor(() => expect(m.coderStatus).toHaveBeenCalled())
    expect(screen.queryByTestId('coder-mode-picker')).not.toBeInTheDocument()
    expect(screen.queryByRole('radio', { name: /Coder/ })).not.toBeInTheDocument()
  })

  it('never offers coder mode in a workflow chat', async () => {
    m.coderStatus.mockResolvedValue(status())
    await openPanel({ workflowID: 'wf-1' })
    expect(screen.queryByTestId('coder-mode-picker')).not.toBeInTheDocument()
    expect(m.coderStatus).not.toHaveBeenCalled()
  })

  it('disables Coder with a monomind-update hint when not ready', async () => {
    m.coderStatus.mockResolvedValue(status({ ready: false, missingCapabilities: ['agent-exec-full-access', 'init-json'] }))
    await openPanel()
    const coder = await screen.findByRole('radio', { name: /Coder/ })
    expect(coder).toBeDisabled()
    expect(screen.getByTestId('coder-not-ready')).toHaveTextContent('needs monomind update (missing: agent-exec-full-access, init-json)')
  })

  it('starts a coder chat in the coder root, without tools, and then locks the mode', async () => {
    m.coderStatus.mockResolvedValue(status())
    m.coderWorkspaceDefault.mockResolvedValue({ path: WS, created: true, git: true, init: { created: ['CLAUDE.md', '.claude/settings.json'], skipped: [] } })
    m.createCoderConversation.mockResolvedValue({ id: 'coder-1', backend: 'agent', mode: 'coder', cwd: WS })
    await openPanel()

    fireEvent.click(await screen.findByRole('radio', { name: /Coder/ }))
    const workspaces = screen.getByRole('radiogroup', { name: 'Coder workspace' })
    expect(within(workspaces).getByRole('radio', { name: /Profile folder/ })).toHaveAttribute('aria-checked', 'true')
    expect(within(workspaces).getByRole('radio', { name: /Profile folder/ })).toHaveTextContent('Profile folder (default): Work Stuff')
    expect(within(workspaces).getByRole('radio', { name: /Profile folder/ })).toHaveTextContent(WS)
    expect(await within(workspaces).findByRole('radio', { name: /recent-proj/ })).toBeInTheDocument()

    await sendMessage('write hello.py and run it')
    await waitFor(() => expect(m.createCoderConversation).toHaveBeenCalledWith('claude', 'sonnet', '', WS, false))
    expect(m.coderWorkspaceDefault).toHaveBeenCalledTimes(1)
    expect(m.coderWorkspaceDefault).toHaveBeenCalledWith('claude')
    expect(m.createChatConversation).not.toHaveBeenCalled()
    await waitFor(() => expect(m.startChatTurn).toHaveBeenCalledWith('coder-1', expect.any(String), 'write hello.py and run it', false, false))

    // Mode locked: the picker is gone, the coder header shows the folder.
    expect(screen.queryByTestId('coder-mode-picker')).not.toBeInTheDocument()
    expect(screen.getByTestId('coder-cwd')).toHaveTextContent(WS)
    expect(screen.getByTestId('coder-init-note')).toHaveTextContent(`Created coder root ${WS} · created CLAUDE.md, .claude/settings.json · git repository`)
    fireEvent.click(screen.getByRole('button', { name: /Open folder/ }))
    expect(m.openPathWithOS).toHaveBeenCalledWith(WS)

    // Stop (button, then Esc) stops the running coder turn.
    fireEvent.click(screen.getByRole('button', { name: 'Stop generating' }))
    await waitFor(() => expect(m.stopChatTurn).toHaveBeenCalledWith('coder-1', expect.any(String)))
  })

  it('Escape stops a running coder turn instead of closing the panel', async () => {
    m.coderStatus.mockResolvedValue(status())
    m.createCoderConversation.mockResolvedValue({ id: 'coder-2', backend: 'agent', mode: 'coder', cwd: '/w/recent-proj' })
    const onClose = vi.fn()
    await openPanel({ onClose })
    fireEvent.click(await screen.findByRole('radio', { name: /Coder/ }))
    fireEvent.click(await screen.findByRole('radio', { name: /recent-proj/ }))
    await sendMessage('go')
    await waitFor(() => expect(m.createCoderConversation).toHaveBeenCalledWith('claude', 'sonnet', '', '/w/recent-proj', false))
    expect(m.coderWorkspaceDefault).not.toHaveBeenCalled()
    await screen.findByRole('button', { name: 'Stop generating' })
    fireEvent.keyDown(window, { key: 'Escape' })
    await waitFor(() => expect(m.stopChatTurn).toHaveBeenCalledWith('coder-2', expect.any(String)))
    expect(onClose).not.toHaveBeenCalled()
  })

  it('uses a folder chosen with the native picker', async () => {
    m.coderStatus.mockResolvedValue(status())
    m.pickCoderFolder.mockResolvedValue('/home/u/src/my-repo')
    m.createCoderConversation.mockResolvedValue({ id: 'coder-3', backend: 'agent', mode: 'coder', cwd: '/home/u/src/my-repo' })
    await openPanel()
    fireEvent.click(await screen.findByRole('radio', { name: /Coder/ }))
    fireEvent.click(screen.getByRole('radio', { name: /Choose another folder/ }))
    expect(await screen.findByText('/home/u/src/my-repo')).toBeInTheDocument()
    await sendMessage('hi')
    await waitFor(() => expect(m.createCoderConversation).toHaveBeenCalledWith('claude', 'sonnet', '', '/home/u/src/my-repo', false))
  })

  it('falls back to claude when coder status predates per-runtime readiness', async () => {
    m.coderStatus.mockResolvedValue(status())
    m.coderWorkspaceDefault.mockResolvedValue({ path: WS, created: false, git: true, init: { created: [], skipped: [] } })
    m.createCoderConversation.mockResolvedValue({ id: 'coder-4', backend: 'agent', mode: 'coder', cwd: WS })
    await openPanel()
    const runtimeSelect = screen.getByTitle('Locally installed AI agent (via monomind)')
    expect(runtimeSelect).toHaveValue('codex')
    fireEvent.click(await screen.findByRole('radio', { name: /Coder/ }))
    await waitFor(() => expect(runtimeSelect).toHaveValue('claude'))
    expect([...runtimeSelect.options].map(o => o.value)).toEqual(['claude'])
    expect(screen.queryByTestId('coder-fidelity-note')).not.toBeInTheDocument()
    await sendMessage('who are you')
    await waitFor(() => expect(m.createCoderConversation).toHaveBeenCalledWith('claude', 'sonnet', '', WS, false))
  })

  it('coder mode offers coder-ready runtimes and creates the conversation on the chosen one with its own model', async () => {
    m.coderStatus.mockResolvedValue(status({ runtimes: [
      { id: 'claude', installed: true, fullAccess: true, ready: true, toolActivity: 'full', effort: true, reportsCost: true, initTarget: 'claude' },
      { id: 'codex', installed: true, fullAccess: true, ready: true, toolActivity: 'start-only', effort: true, reportsCost: false, initTarget: 'codex' },
      { id: 'kimicode', installed: false, fullAccess: true, ready: false, toolActivity: 'full' },
    ] }))
    m.coderWorkspaceDefault.mockResolvedValue({ path: WS, created: true, git: true, init: { created: ['AGENTS.md', '.codex/config.toml', 'CLAUDE.md', '.claude/skills/x'], skipped: [] } })
    m.createCoderConversation.mockResolvedValue({ id: 'coder-5', backend: 'agent', mode: 'coder', cwd: WS })
    const { api } = await import('../services/api.js')
    api.getAgentRuntimeModels.mockImplementation(rt => Promise.resolve(rt === 'codex'
      ? [{ id: 'gpt-5-codex', effort_levels: ['low', 'medium', 'high'] }]
      : [{ id: 'sonnet', effort_levels: ['low', 'high'] }]))
    await openPanel()
    fireEvent.click(await screen.findByRole('radio', { name: /Coder/ }))
    const runtimeSelect = screen.getByTitle(/Coding agent for this Coder chat/)
    expect(runtimeSelect).not.toBeDisabled()
    expect([...runtimeSelect.options].map(o => o.value)).toEqual(['codex', 'claude'])
    expect(screen.getByTestId('coder-runtimes')).toHaveTextContent('kimicode not installed')

    // Claude first, then back to codex: the model list follows the runtime,
    // so claude's sonnet never reaches codex.
    fireEvent.change(runtimeSelect, { target: { value: 'claude' } })
    await waitFor(() => expect(screen.getByTitle('Model available for the selected agent runtime')).toHaveValue('sonnet'))
    fireEvent.change(runtimeSelect, { target: { value: 'codex' } })
    const modelSelect = screen.getByTitle('Model available for the selected agent runtime')
    await waitFor(() => expect(modelSelect).toHaveValue('gpt-5-codex'))
    expect(screen.getByTestId('coder-fidelity-note')).toHaveTextContent('codex shows commands, not every result')
    expect(screen.getByTestId('coder-mode-picker')).toHaveTextContent('codex will run commands and change files in this folder without asking')
    fireEvent.change(screen.getByLabelText('Effort level'), { target: { value: 'high' } })

    await sendMessage('build it')
    await waitFor(() => expect(m.createCoderConversation).toHaveBeenCalledWith('codex', 'gpt-5-codex', 'high', WS, false))
    expect(m.coderWorkspaceDefault).toHaveBeenCalledWith('codex')
    expect(screen.getByTestId('coder-init-note')).toHaveTextContent('created AGENTS.md, .codex/config.toml and 2 more')
    api.getAgentRuntimeModels.mockResolvedValue([{ id: 'sonnet' }])
  })

  it('switches off a runtime coder mode cannot run on', async () => {
    m.coderStatus.mockResolvedValue(status({ runtimes: [
      { id: 'codex', installed: true, fullAccess: false, ready: false },
      { id: 'claude', installed: true, fullAccess: true, ready: true, toolActivity: 'full' },
    ] }))
    await openPanel()
    const runtimeSelect = screen.getByTitle('Locally installed AI agent (via monomind)')
    expect(runtimeSelect).toHaveValue('codex')
    fireEvent.click(await screen.findByRole('radio', { name: /Coder/ }))
    await waitFor(() => expect(runtimeSelect).toHaveValue('claude'))
    expect([...runtimeSelect.options].map(o => o.value)).toEqual(['claude'])
    expect(screen.getByTestId('coder-runtimes')).toHaveTextContent('codex no full access in this monomind')
    // Back to the assistant: every installed runtime again.
    fireEvent.click(screen.getByRole('radio', { name: /Assistant/ }))
    expect([...runtimeSelect.options].map(o => o.value)).toEqual(['codex', 'claude'])
  })

  it('offers no Coder choice when no runtime is ready', async () => {
    m.coderStatus.mockResolvedValue(status({ ready: false, runtimes: [{ id: 'claude', installed: false, fullAccess: true, ready: false }] }))
    await openPanel()
    expect(await screen.findByRole('radio', { name: /Coder/ })).toBeDisabled()
    expect(screen.getByTestId('coder-not-ready')).toHaveTextContent('needs a coding runtime')
  })

  it('an assistant chat still creates an assistant conversation with tools', async () => {
    m.coderStatus.mockResolvedValue(status())
    m.createChatConversation.mockResolvedValue({ id: 'a-1', backend: 'agent', mode: 'assistant' })
    await openPanel()
    await screen.findByRole('radio', { name: /Assistant/ })
    await sendMessage('hello')
    await waitFor(() => expect(m.createChatConversation).toHaveBeenCalled())
    expect(m.createCoderConversation).not.toHaveBeenCalled()
    await waitFor(() => expect(m.startChatTurn).toHaveBeenCalledWith('a-1', expect.any(String), 'hello', true, true))
    expect(screen.queryByTestId('coder-header')).not.toBeInTheDocument()
  })

  it('a reopened coder conversation shows its folder, and history rows carry a coder badge', async () => {
    m.coderStatus.mockResolvedValue(status())
    m.listChatConversations.mockResolvedValue({
      items: [
        { id: 'coder-9', backend: 'agent', workflowContext: 'general', runtimeId: 'claude', model: 'sonnet', mode: 'coder', cwd: WS, updatedAt: '2026-09-27T10:00:00Z' },
        { id: 'a-9', backend: 'agent', workflowContext: 'general', runtimeId: 'claude', model: 'sonnet', mode: 'assistant', cwd: '', updatedAt: '2026-09-26T10:00:00Z' },
      ],
    })
    m.getChatTurns.mockResolvedValue({ items: [{ id: 't1', prompt: 'earlier prompt', status: 'completed' }] })
    await openPanel()
    await screen.findByText('earlier prompt')
    expect(screen.getByTestId('coder-cwd')).toHaveTextContent(WS)
    expect(screen.queryByTestId('coder-mode-picker')).not.toBeInTheDocument()

    fireEvent.click(screen.getByTitle('Past sessions'))
    const rows = await screen.findAllByRole('option')
    expect(within(rows[0]).getByTestId('coder-badge')).toBeInTheDocument()
    expect(within(rows[0]).getByText('monoagent-coder')).toBeInTheDocument()
    expect(within(rows[1]).queryByTestId('coder-badge')).not.toBeInTheDocument()
  })

  it('a reopened conversation whose turn is still running here goes back to live, not a snapshot', async () => {
    m.coderStatus.mockResolvedValue(status())
    m.listChatConversations.mockResolvedValue({
      items: [{ id: 'coder-9', backend: 'agent', workflowContext: 'general', runtimeId: 'claude', model: 'sonnet', mode: 'coder', cwd: WS, updatedAt: '2026-09-27T10:00:00Z' }],
    })
    m.getChatTurns.mockResolvedValue({ items: [
      { id: 't2', prompt: 'still running', status: 'active', ownedByThisInstance: true },
      { id: 't1', prompt: 'earlier prompt', status: 'completed' },
    ] })
    render(<AIChatPanel workflowID="general" isOpen={true} onClose={() => {}} />)
    await screen.findByText('still running')
    const { api } = await import('../services/api.js')
    await waitFor(() => expect(api.getChatEvents).toHaveBeenCalledWith('coder-9', 't2', 0, 200))
    expect(await screen.findByRole('button', { name: /stop/i })).toBeInTheDocument()
  })

  it('summarizes a real monomind init (hundreds of files) to the key ones', () => {
    const created = ['.claude', '.claude/skills', ...Array.from({ length: 200 }, (_, i) => `.claude/skills/s${i}`), '.claude/settings.json', '.mcp.json', 'CLAUDE.md']
    expect(initSummary(created)).toBe('CLAUDE.md, .claude/settings.json, .mcp.json and 202 more')
    expect(initSummary(['a', 'b'])).toBe('a, b')
    expect(initSummary(['AGENTS.md', 'opencode.json', 'x'], 'opencode')).toBe('AGENTS.md, opencode.json and 1 more')
    expect(initSummary(['GEMINI.md', '.gemini/settings.json'], 'antigravity')).toBe('GEMINI.md, .gemini/settings.json')
    expect(initSummary(['AGENTS.md', '.kimi-code/mcp.json'], 'kimicode')).toBe('AGENTS.md, .kimi-code/mcp.json')
    expect(initSummary(['.clinerules/monomind.md', 'AGENTS.md', 'x'], 'cline')).toBe('.clinerules/monomind.md, AGENTS.md and 1 more')
    expect(initSummary(['CONVENTIONS.md', '.aider.conf.yml'], 'aider')).toBe('CONVENTIONS.md, .aider.conf.yml')
    expect(initSummary(['AGENTS.md'], 'dsh')).toBe('AGENTS.md')
    expect(initSummary(['AGENTS.md', 'x'], 'pi')).toBe('AGENTS.md and 1 more')
    for (const rt of ['pi', 'pi-rpc', 'dsh', 'grok', 'copilot', 'qwen', 'crush', 'hermes']) {
      expect(keyInitFiles(rt), rt).toEqual(['AGENTS.md'])
    }
    render(<CoderInitNote workspace={{ path: '/w/grok', created: true, init: { created: ['AGENTS.md'], skipped: [] } }} runtime="grok" />)
    const note = screen.getByTestId('coder-init-note')
    expect(note).toHaveTextContent('created AGENTS.md')
    expect(note).not.toHaveTextContent(/CLAUDE|\.claude|\.mcp\.json|show all/)
  })
})

// With coder bubbles on (#227), the panel hands coder chats to them.
describe('AIChatPanel with coder bubbles', () => {
  it('opens a new coder bubble instead of switching the panel to Coder', async () => {
    m.coderStatus.mockResolvedValue(status())
    const onOpenCoderChat = vi.fn()
    await openPanel({ onOpenCoderChat })
    fireEvent.click(await screen.findByRole('radio', { name: /Coder/ }))
    expect(onOpenCoderChat).toHaveBeenCalledWith(null)
    expect(screen.getByRole('radio', { name: /Assistant/ })).toHaveAttribute('aria-checked', 'true')
    expect(screen.queryByRole('radiogroup', { name: 'Coder workspace' })).not.toBeInTheDocument()
  })

  it('opens a past coder session as its bubble and does not auto-continue it here', async () => {
    m.coderStatus.mockResolvedValue(status())
    const coderConv = { id: 'coder-9', backend: 'agent', mode: 'coder', cwd: '/w/proj', workflowContext: 'general', runtimeId: 'claude', updatedAt: '2026-09-29T10:00:00Z' }
    const plainConv = { id: 'plain-1', backend: 'agent', mode: 'assistant', workflowContext: 'general', runtimeId: 'claude', updatedAt: '2026-09-28T10:00:00Z' }
    m.listChatConversations.mockResolvedValue({ items: [coderConv, plainConv] })
    const onOpenCoderChat = vi.fn()
    await openPanel({ onOpenCoderChat })
    // The newest non-coder conversation is the one continued here.
    await waitFor(() => expect(m.getChatTurns).toHaveBeenCalledWith('plain-1', '', 50))
    expect(m.getChatTurns).not.toHaveBeenCalledWith('coder-9', '', 50)

    fireEvent.click(screen.getByTitle('Past sessions'))
    const options = await screen.findAllByRole('option')
    fireEvent.click(options.find(o => o.textContent.includes('proj')))
    expect(onOpenCoderChat).toHaveBeenCalledWith(coderConv)
  })
})
