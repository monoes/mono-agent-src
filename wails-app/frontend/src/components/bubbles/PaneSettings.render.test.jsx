// @vitest-environment jsdom
import React, { useEffect } from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup, within } from '@testing-library/react'
import '../../i18n.js'

// The pane's header chips and settings popover: what each control reads, and
// that the only things it changes are the access level and the org mode.
const { api, chatAccess, budget, mockConfirm } = vi.hoisted(() => ({
  mockConfirm: vi.fn(),
  chatAccess: { create: vi.fn(), setAccess: vi.fn(), record: vi.fn() },
  budget: { forTask: vi.fn(), list: vi.fn(), lower: vi.fn(), spendCap: vi.fn() },
  api: {
    getChatMissionToggle: vi.fn(() => Promise.resolve({})), // the Mission switch in the pane header
    getChatTurns: vi.fn(), getChatEvents: vi.fn(), startChatTurn: vi.fn(), stopChatTurn: vi.fn(), coderStatus: vi.fn(),
    coderWorkspaceList: vi.fn(), getAgentRuntimeModels: vi.fn(), getProfiles: vi.fn(), listChatConversations: vi.fn(),
    browserPermissions: vi.fn(), setChatOrgMode: vi.fn(), getActiveProfile: vi.fn(),
    getCoderSessionSettings: vi.fn(), setCoderSessionSettings: vi.fn(),
  },
}))
vi.mock('../../services/api.js', () => ({
  api, notify: vi.fn(), onChatEvent: () => () => {}, onOrgEvent: () => () => {}, onOrgRunStatus: () => () => {}, newOrgEventsStreamId: () => 's',
}))
vi.mock('../../lib/chatAccessApi.js', () => ({ chatAccessApi: chatAccess }))
vi.mock('../settings/aitaskBudgetApi.js', () => ({ aitaskBudgetApi: budget, money: n => `$${Number(n || 0).toFixed(2)}` }))
vi.mock('../ConfirmDialog.jsx', () => ({ confirm: (...a) => mockConfirm(...a) }))

import CoderBubbles from './CoderBubbles.jsx'
import { useCoderBubbles } from './useCoderBubbles.js'

let store
const navigate = vi.fn()
function Harness({ conv, task }) {
  store = useCoderBubbles()
  useEffect(() => { if (!conv) return; store.openConversation(conv); if (task) store.openDraft({ conversationId: conv.id, task }) }, []) // eslint-disable-line react-hooks/exhaustive-deps
  return <CoderBubbles store={store} onNavigate={navigate} />
}
const overlay = () => screen.queryByTestId('bubble-overlay')
const popover = () => screen.queryByTestId('pane-settings')
const gear = () => screen.getByRole('button', { name: 'Chat settings' })
const scoped = { id: 's1', access: 'scoped', runtimeId: 'claude', model: 'opus', effort: 'high', title: 'Summarise my week' }
const coding = { id: 'k1', access: 'coding', runtimeId: 'codex', model: 'gpt-5', cwd: '/home/u/proj' }
const records = { s1: { access: 'scoped', title: 'Summarise my week' }, k1: { access: 'coding', cwd: '/home/u/proj' }, k2: { access: 'coding', cwd: '/w' }, k7: { access: 'coding' }, 'conv-x': { access: 'coding' } }
const open = async (conv, task) => {
  render(<Harness conv={conv} task={task} />)
  await waitFor(() => expect(overlay()).toBeInTheDocument())
}
const noCap = { bounded: false, capUsd: 0 }
const taskCap = (left, total) => ({ bounded: true, source: 'task', capUsd: left, totalUsd: total, taskId: 't1' })
const taskLine = (left, total) => `Up to ${left} for the next message: what is left of this task's ${total} cap.`
const sessionView = {
  conversationId: 'k1', role: 'solo', unattended: true, access: 'coding', operatorOnly: [],
  allowed: { autonomy_preset: ['ask-always', 'ask-on-risk', 'decide-and-log'], gap_classes: ['ambiguity'], mesh_accept: ['none', 'project', 'subtree'] },
  settings: { checkin_minutes: 15, skip_if_unchanged: true, autonomy_preset: 'ask-on-risk', autonomy_classes: {}, max_children: 4, max_concurrent: 2, max_depth: 2, checkin_limit: 48, mesh_visible: true, mesh_alias: '', mesh_accept: 'project' },
}
const kinds = [{ kind: 'apply', label: 'Apply', level: 'ask', isDefault: true }, { kind: 'chat', label: 'Chat', level: 'ask', isDefault: true }, { kind: 'coder', label: 'Coder', level: 'dev', isDefault: false }]

beforeEach(() => {
  vi.clearAllMocks()
  Element.prototype.scrollTo = function scrollTo() {}
  localStorage.clear()
  api.getChatTurns.mockResolvedValue({ items: [] })
  api.getChatEvents.mockResolvedValue({ items: [], hasMore: false })
  api.coderStatus.mockResolvedValue({ enabled: true, ready: true })
  api.coderWorkspaceList.mockResolvedValue([])
  api.getProfiles.mockResolvedValue([])
  api.getActiveProfile.mockResolvedValue(null)
  api.getCoderSessionSettings.mockResolvedValue(sessionView)
  api.listChatConversations.mockResolvedValue({ items: [] })
  api.browserPermissions.mockResolvedValue({ kinds, catalog: [{ id: 'ask', label: 'Ask first' }, { id: 'dev', label: 'Developer' }] })
  api.setChatOrgMode.mockResolvedValue({ ok: true })
  budget.list.mockResolvedValue({ caps: [{ scope: 'global', key: '', capUsd: 5, usedUsd: 1.5 }], spentUsd: 1.5 })
  budget.forTask.mockResolvedValue({ taskId: 't1', capUsd: 2, spentUsd: 0.5, reported: true })
  chatAccess.setAccess.mockImplementation(async (id, level) => ({ conversation: { id, access: level, cwd: '/home/u/profile' }, previousAccess: 'coding', lowered: null }))
  mockConfirm.mockResolvedValue(true)
  budget.spendCap.mockResolvedValue(noCap)
  chatAccess.record.mockImplementation(async id => records[id] || { access: 'coding' })
})
afterEach(cleanup)

describe('header chips', () => {
  it('shows the access level, the title and the runtime of a scoped chat', async () => {
    await open(scoped)
    expect(screen.getByTestId('chip-access')).toHaveTextContent('Access: Scoped')
    expect(within(overlay()).getByText('Summarise my week')).toBeInTheDocument()
    expect(within(overlay()).getByText(/Claude.*opus/i)).toBeInTheDocument()
    // A scoped chat starts no workers: no org stage, no Org control.
    expect(screen.queryByRole('button', { name: /^Org$/ })).toBeNull()
    expect(document.querySelector('[data-testid="stage-node"]')).toBeNull()
  })

  it('shows a coding chat with its org control and its stage', async () => {
    await open(coding)
    expect(screen.getByTestId('chip-access')).toHaveTextContent('Access: Coding')
    expect(screen.getByRole('button', { name: /Org/ })).toBeInTheDocument()
    expect(document.querySelector('[data-testid="stage-node"]')).not.toBeNull()
  })

  it('reads a bubble opened with no level from the backend record, and a history row needs no lookup', async () => {
    chatAccess.record.mockResolvedValue({ access: 'workspace', orgMode: 'dynamic', cwd: '/w' })
    await open({ id: 'k1' })
    await waitFor(() => expect(screen.getByTestId('chip-access')).toHaveTextContent('Access: Workspace'))
    expect(store.bubbles[0].orgMode).toBe('dynamic')
    cleanup(); chatAccess.record.mockClear()
    await open({ id: 'k3', access: 'scoped', mode: 'assistant', title: 'From history', runtime: 'claude', model: 'm' })
    expect(screen.getByTestId('chip-access')).toHaveTextContent('Access: Scoped')
    expect(chatAccess.record).not.toHaveBeenCalled()
  })

  it('a history row without a level is looked up, not guessed from its mode', async () => {
    chatAccess.record.mockResolvedValue({ access: 'coding', cwd: '/w' })
    await open({ id: 'k4', mode: 'coder' })
    await waitFor(() => expect(chatAccess.record).toHaveBeenCalledWith('k4'))
    await waitFor(() => expect(screen.getByTestId('chip-access')).toHaveTextContent('Access: Coding'))
  })
})

describe('settings popover', () => {
  it('opens with focus inside, lists what the chat runs on, and returns focus on close', async () => {
    await open(scoped)
    fireEvent.click(gear())
    expect(popover()).toBeInTheDocument()
    expect(gear()).toHaveAttribute('aria-expanded', 'true')
    expect(within(popover()).getByText(/opus · high/)).toBeInTheDocument()
    expect(popover().contains(document.activeElement)).toBe(true)
    fireEvent.keyDown(window, { key: 'Escape' })
    await waitFor(() => expect(popover()).toBeNull())
    expect(overlay()).toBeInTheDocument() // the first Esc closed the popover only
    expect(document.activeElement).toBe(gear())
  })

  it('leaves an Escape from inside the History drawer to the drawer: the bubble stays expanded and the key still travels on', async () => {
    await open(coding)
    const drawer = document.createElement('aside'); drawer.setAttribute('data-testid', 'history-panel')
    const input = document.createElement('input'); drawer.appendChild(input); document.body.appendChild(drawer)
    const reached = vi.fn(); document.addEventListener('keydown', reached)
    input.focus()
    fireEvent.keyDown(input, { key: 'Escape' })
    expect(reached).toHaveBeenCalled()
    expect(overlay()).toBeInTheDocument()
    document.removeEventListener('keydown', reached)
    drawer.remove()
    // Focus in the bubble still collapses it.
    fireEvent.keyDown(within(overlay()).getByPlaceholderText('Type a message...'), { key: 'Escape' })
    await waitFor(() => expect(overlay()).toBeNull())
  })

  it('Esc layers: popover first, then collapse, and never stops a running turn', async () => {
    api.getChatTurns.mockResolvedValue({ items: [{ id: 't1', prompt: 'work', status: 'active', ownedByThisInstance: true }] })
    await open(coding)
    await waitFor(() => expect(screen.getByText('work')).toBeInTheDocument())
    fireEvent.click(gear())
    fireEvent.keyDown(window, { key: 'Escape' })
    await waitFor(() => expect(popover()).toBeNull())
    fireEvent.keyDown(window, { key: 'Escape' })
    await waitFor(() => expect(overlay()).toBeNull())
    expect(api.stopChatTurn).not.toHaveBeenCalled()
  })

  // #1766: the row shows the CLI's cap of this chat, never the AI-task budgets of other work.
  const spendText = async (cap, conv = coding) => {
    budget.spendCap.mockResolvedValue(cap)
    await open(conv)
    fireEvent.click(gear())
    const row = await screen.findByTestId('pane-cost')
    await waitFor(() => expect(row).not.toHaveTextContent('Loading'))
    expect(budget.spendCap).toHaveBeenCalledWith(conv.id)
    return row
  }
  it.each([
    [{ bounded: true, source: 'message', capUsd: 0.5 }, 'Up to $0.50 per message (coder budget).'],
    [{ bounded: true, source: 'session', capUsd: 0.25, totalUsd: 3 }, "Up to $0.25 for the next message: what is left of this chat's $3.00 session budget."],
    [{ bounded: true, source: 'task', capUsd: 1.5, totalUsd: 2 }, "Up to $1.50 for the next message: what is left of this task's $2.00 cap."],
    [{ bounded: true, source: 'session', capUsd: 0, totalUsd: 3, usedUp: true }, "This chat's $3.00 session budget is used up: a message does not run."],
    [{ bounded: true, source: 'task', capUsd: 0, totalUsd: 2, usedUp: true }, 'Nothing is left to spend on this task: a message does not run.'],
    [noCap, "No spend cap bounds this chat's messages."],
  ])('says what bounds the next message: %j', async (cap, text) => {
    const row = await spendText(cap)
    expect(within(row).getByText(text)).toBeInTheDocument()
    expect(row).not.toHaveTextContent('global cap')
    expect(row).not.toHaveTextContent('set a global cap')
    expect(screen.queryByTestId('pane-lower')).toBeNull()
  })

  it('says when the runtime cannot hold the cap, and what the org workers may add', async () => {
    let row = await spendText({ bounded: true, source: 'message', capUsd: 0.5, unenforceableOn: 'kilo' })
    expect(row).toHaveTextContent('kilo cannot enforce a spend cap, so this cap is not applied.')
    cleanup()
    row = await spendText({ bounded: true, source: 'session', capUsd: 1, totalUsd: 3, unenforceableOn: 'kilo' })
    expect(row).toHaveTextContent('kilo cannot enforce a spend cap, so a message under this cap does not run.')
    cleanup()
    row = await spendText({ bounded: true, source: 'message', capUsd: 0.5, workersCapUsd: 2 })
    expect(row).toHaveTextContent('Org workers: up to $2.00 more per message.')
  })

  it('says so when the cap cannot be read, and links to Settings with no raise control', async () => {
    budget.spendCap.mockRejectedValue(new Error('boom'))
    await open(coding)
    fireEvent.click(gear())
    await waitFor(() => expect(screen.getByTestId('pane-cost')).toHaveTextContent('The spend cap could not be read.'))
    fireEvent.click(within(screen.getByTestId('pane-cost')).getByRole('button', { name: /budgets in Settings/i }))
    expect(navigate).toHaveBeenCalledWith('settings')
  })

  it('reads the browser level for the chat kind when scoped and for the coder kind otherwise, read-only', async () => {
    await open(scoped)
    fireEvent.click(gear())
    await waitFor(() => expect(within(screen.getByTestId('pane-browser')).getByText('Ask first')).toBeInTheDocument())
    cleanup()
    await open(coding)
    fireEvent.click(gear())
    await waitFor(() => expect(within(screen.getByTestId('pane-browser')).getByText('Developer')).toBeInTheDocument())
    expect(within(screen.getByTestId('pane-browser')).queryByRole('combobox')).toBeNull()
  })

  it('shows the unattended state of a coding chat with its access level, and reads no session settings for a scoped one', async () => {
    await open(scoped)
    fireEvent.click(gear())
    expect(screen.queryByTestId('pane-unattended')).toBeNull()
    expect(api.getCoderSessionSettings).not.toHaveBeenCalled()
    cleanup()
    await open(coding)
    fireEvent.click(gear())
    const row = await screen.findByTestId('pane-unattended')
    expect(row).toHaveTextContent('Unattended: on')
    expect(api.getCoderSessionSettings).toHaveBeenCalledWith('k1')
  })

  it('shows the assistant tools for a scoped chat with a link, not a toggle, and the org switch for a coding chat only', async () => {
    await open(scoped)
    fireEvent.click(gear())
    expect(screen.getByTestId('pane-tools')).toHaveTextContent('MonoAgent tools on, workflow runs on')
    expect(within(screen.getByTestId('pane-tools')).queryByRole('switch')).toBeNull()
    expect(within(popover()).queryByRole('switch')).toBeNull()
    cleanup()
    await open(coding)
    fireEvent.click(gear())
    expect(screen.queryByTestId('pane-tools')).toBeNull()
    const sw = within(popover()).getByRole('switch', { name: 'Org mode' })
    expect(sw).toHaveAttribute('aria-checked', 'false')
    fireEvent.click(sw)
    await waitFor(() => expect(api.setChatOrgMode).toHaveBeenCalledWith('k1', 'dynamic'))
    await waitFor(() => expect(sw).toHaveAttribute('aria-checked', 'true'))
    fireEvent.click(sw)
    await waitFor(() => expect(api.setChatOrgMode).toHaveBeenLastCalledWith('k1', 'solo'))
  })
})

describe('access control', () => {
  it('lowers at once, without asking, and keeps the new level', async () => {
    await open(coding)
    fireEvent.click(gear())
    fireEvent.change(within(popover()).getByLabelText('Access'), { target: { value: 'scoped' } })
    await waitFor(() => expect(chatAccess.setAccess).toHaveBeenCalledWith('k1', 'scoped', 'coding', false))
    expect(mockConfirm).not.toHaveBeenCalled()
    await waitFor(() => expect(store.bubbles[0].access).toBe('scoped'))
    expect(screen.getByTestId('chip-access')).toHaveTextContent('Access: Scoped')
    expect(within(popover()).getByText('Access is now Scoped.')).toBeInTheDocument()
  })

  it('names the processes a lowering could not stop', async () => {
    chatAccess.setAccess.mockResolvedValue({
      conversation: { id: 'k1', access: 'scoped' }, previousAccess: 'coding',
      lowered: { stopped: [], survivors: [{ pid: 4242, command: 'node server.js', reason: 'owned by another user' }], workersDropped: false, taskRevoked: false },
    })
    await open(coding)
    fireEvent.click(gear())
    fireEvent.change(within(popover()).getByLabelText('Access'), { target: { value: 'scoped' } })
    const alert = await screen.findByTestId('pane-survivors')
    expect(alert).toHaveAttribute('role', 'alert')
    expect(alert).toHaveTextContent('1 process could not be stopped and is still running')
    expect(alert).toHaveTextContent('4242 node server.js (owned by another user)')
  })

  it('raising asks first, stating what the level allows, and changes nothing when declined', async () => {
    mockConfirm.mockResolvedValue(false)
    await open(coding)
    fireEvent.click(gear())
    fireEvent.change(within(popover()).getByLabelText('Access'), { target: { value: 'full' } })
    await waitFor(() => expect(mockConfirm).toHaveBeenCalled())
    const [message, opts] = mockConfirm.mock.calls[0]
    expect(opts.title).toBe('Raise access to Full?')
    expect(message).toContain('Only you can raise it')
    expect(message).toContain('The same as Workspace')
    expect(opts.danger).toBe(true)
    expect(chatAccess.setAccess).not.toHaveBeenCalled()
    expect(store.bubbles[0].access).toBe('coding')
  })

  it('raises after the confirmation and shows the folder a scoped chat gets', async () => {
    await open(scoped)
    fireEvent.click(gear())
    fireEvent.change(within(popover()).getByLabelText('Access'), { target: { value: 'coding' } })
    await waitFor(() => expect(chatAccess.setAccess).toHaveBeenCalledWith('s1', 'coding', 'scoped', true))
    await waitFor(() => expect(store.bubbles[0]).toMatchObject({ access: 'coding', cwd: '/home/u/profile' }))
  })

  it('says plainly when the CLI refuses a raise as operator-only', async () => {
    const err = new Error('operator_only: raising a conversation is the operator\'s')
    err.code = 'operator_only'
    chatAccess.setAccess.mockRejectedValue(err)
    await open(coding)
    fireEvent.click(gear())
    fireEvent.change(within(popover()).getByLabelText('Access'), { target: { value: 'full' } })
    expect(await screen.findByText(/Only you, in this window, can raise access/)).toBeInTheDocument()
    expect(store.bubbles[0].access).toBe('coding')
  })

  const restoreFrom = (b) => {
    localStorage.setItem('monoagent:coderBubbles:v1', JSON.stringify({ side: 'right', bubbles: [b] }))
    render(<Harness />)
  }
  const openRestored = async (key) => {
    await waitFor(() => expect(document.querySelector(`[data-bubble="${key}"]`)).toBeInTheDocument())
    fireEvent.click(document.querySelector(`[data-bubble="${key}"]`))
    await waitFor(() => expect(overlay()).toBeInTheDocument())
  }

  it('a stale cache saying full does not turn a raise into a lowering: the record says scoped, so workspace asks', async () => {
    chatAccess.record.mockResolvedValue({ access: 'scoped', cwd: '' })
    restoreFrom({ conversationId: 'k5', cwd: '', model: 'm', runtime: 'claude', access: 'full' })
    await openRestored('k5')
    await waitFor(() => expect(screen.getByTestId('chip-access')).toHaveTextContent('Access: Scoped'))
    fireEvent.click(gear())
    fireEvent.change(within(popover()).getByLabelText('Access'), { target: { value: 'workspace' } })
    await waitFor(() => expect(mockConfirm).toHaveBeenCalled())
    await waitFor(() => expect(chatAccess.setAccess).toHaveBeenCalledWith('k5', 'workspace', 'scoped', true))
  })

  it('a crafted stored level is not believed either, and an old entry with no level is looked up', async () => {
    chatAccess.record.mockResolvedValue({ access: 'coding', cwd: '/w' })
    restoreFrom({ conversationId: 'k6', cwd: '/w', model: 'm', runtime: 'claude' })
    await openRestored('k6')
    await waitFor(() => expect(chatAccess.record).toHaveBeenCalledWith('k6'))
    await waitFor(() => expect(screen.getByTestId('chip-access')).toHaveTextContent('Access: Coding'))
  })

  it('shows access unknown and keeps the controls and the composer off until the lookup answers', async () => {
    let answer
    chatAccess.record.mockImplementation(() => new Promise(r => { answer = r }))
    restoreFrom({ conversationId: 'k8', cwd: '/w', model: 'm', runtime: 'claude', access: 'full' })
    await openRestored('k8')
    expect(screen.getByTestId('chip-access')).toHaveTextContent('Access: Unknown')
    expect(within(overlay()).getByPlaceholderText('Type a message...')).toBeDisabled()
    fireEvent.click(gear())
    expect(within(popover()).getByLabelText('Access')).toBeDisabled()
    answer({ access: 'coding', cwd: '/w' })
    await waitFor(() => expect(within(popover()).getByLabelText('Access')).not.toBeDisabled())
    expect(within(overlay()).getByPlaceholderText('Type a message...')).not.toBeDisabled()
  })

  it('a failed lookup leaves the chat unusable with a retry, and sends nothing', async () => {
    chatAccess.record.mockRejectedValueOnce(new Error('not_found'))
    restoreFrom({ conversationId: 'k9', cwd: '/w', model: 'm', runtime: 'claude' })
    await openRestored('k9')
    const retry = await screen.findByRole('button', { name: 'Try again' })
    expect(within(overlay()).getByPlaceholderText('Type a message...')).toBeDisabled()
    chatAccess.record.mockResolvedValue({ access: 'coding', cwd: '/w' })
    fireEvent.click(retry)
    await waitFor(() => expect(within(overlay()).getByPlaceholderText('Type a message...')).not.toBeDisabled())
  })

  it('when the backend says the level changed, shows the real one and changes nothing', async () => {
    chatAccess.setAccess.mockResolvedValue({ ok: false, code: 'access_changed', current: 'scoped' })
    await open(coding)
    fireEvent.click(gear())
    fireEvent.change(within(popover()).getByLabelText('Access'), { target: { value: 'full' } })
    expect(await screen.findByText('Its access had already changed to Scoped. Nothing was changed.')).toBeInTheDocument()
    expect(store.bubbles[0].access).toBe('scoped')
    expect(screen.getByTestId('chip-access')).toHaveTextContent('Access: Scoped')
  })
})

describe('lowering the cap from the pane', () => {
  const task = { taskId: 't1', goal: 'do it', status: 'running', kind: 'generic' }
  const lowerBox = () => within(screen.getByTestId('pane-cost')).getByLabelText('New cap for this chat (USD)')
  const lowerBtn = () => within(screen.getByTestId('pane-cost')).getByRole('button', { name: 'Lower cap' })
  const openPane = async (t = task) => { await open(coding, t); fireEvent.click(gear()); await waitFor(() => expect(screen.getByTestId('pane-lower')).toBeInTheDocument()) }
  beforeEach(() => { budget.spendCap.mockResolvedValue(taskCap(1.5, 2)) })

  it('lowers after a confirm that says it only lowers, then re-reads the spend and cap', async () => {
    budget.lower.mockResolvedValue({ taskId: 't1', capUsd: 1 })
    await openPane()
    budget.spendCap.mockResolvedValue(taskCap(0.5, 1))
    fireEvent.change(lowerBox(), { target: { value: '1' } })
    fireEvent.click(lowerBtn())
    await waitFor(() => expect(budget.lower).toHaveBeenCalledWith('task', 't1', 1))
    expect(mockConfirm.mock.calls[0][0]).toBe('This only lowers the cap for this chat. Raising it is done in Settings > AI task budgets.')
    await waitFor(() => expect(within(screen.getByTestId('pane-cost')).getByText(taskLine('$0.50', '$1.00'))).toBeInTheDocument())
    expect(within(screen.getByTestId('pane-cost')).getByText('Cap lowered to $1.00.')).toBeInTheDocument()
  })

  it.each([['2'], ['3'], ['-1'], ['NaN'], ['1e2'], [' 1'], ['0.5'], ['0.4']])('refuses %j in the pane without calling anything', async (v) => {
    await openPane()
    fireEvent.change(lowerBox(), { target: { value: v } })
    fireEvent.click(lowerBtn())
    await waitFor(() => expect(within(screen.getByTestId('pane-cost')).getByRole('alert')).toBeInTheDocument())
    expect(budget.lower).not.toHaveBeenCalled()
    expect(mockConfirm).not.toHaveBeenCalled()
  })

  it('shows the typed refusal plainly and keeps the old cap', async () => {
    budget.lower.mockRejectedValue(Object.assign(new Error('a cap can only be lowered here'), { code: 'cap_raise_refused' }))
    await openPane()
    fireEvent.change(lowerBox(), { target: { value: '1' } })
    fireEvent.click(lowerBtn())
    expect(await within(screen.getByTestId('pane-cost')).findByRole('alert')).toHaveTextContent('Refused: a cap can only be lowered here')
    expect(within(screen.getByTestId('pane-cost')).getByText(taskLine('$1.50', '$2.00'))).toBeInTheDocument()
  })

  it('does nothing when the confirm is declined', async () => {
    mockConfirm.mockResolvedValue(false)
    await openPane()
    fireEvent.change(lowerBox(), { target: { value: '1' } })
    fireEvent.click(lowerBtn())
    await waitFor(() => expect(mockConfirm).toHaveBeenCalled())
    expect(budget.lower).not.toHaveBeenCalled()
  })

  // A conversation cap bounds only a task's turns, so a chat with no task is offered none.
  it('offers no lowering on a chat with no task, even under a global AI-task cap', async () => {
    budget.spendCap.mockResolvedValue({ bounded: true, source: 'message', capUsd: 0.5 })
    await open(coding)
    fireEvent.click(gear())
    await waitFor(() => expect(screen.getByTestId('pane-cost')).toHaveTextContent('per message'))
    expect(screen.queryByTestId('pane-lower')).toBeNull()
    expect(screen.queryByTestId('pane-lower-note')).toBeNull()
    expect(budget.list).not.toHaveBeenCalled()
  })

  it('says no cap applies, with no control, to a task with none', async () => {
    budget.forTask.mockResolvedValue({ taskId: 't1', capUsd: 0, spentUsd: 0, reported: false })
    await open(coding, task)
    fireEvent.click(gear())
    expect(await screen.findByTestId('pane-lower-note')).toHaveTextContent('No cap applies yet: set a global cap in Settings > AI task budgets.')
    expect(screen.queryByTestId('pane-lower')).toBeNull()
  })

  it('shows a confined Apply chat\'s task cap read-only', async () => {
    await open(coding, { taskId: 't9', goal: 'Apply', status: 'running', kind: 'application-apply' })
    fireEvent.click(gear())
    await waitFor(() => expect(within(screen.getByTestId('pane-cost')).getByText(/this task's .* cap/)).toBeInTheDocument())
    expect(await screen.findByTestId('pane-lower-note')).toHaveTextContent('shown here only')
    expect(screen.queryByTestId('pane-lower')).toBeNull()
  })
})

describe('a confined application-apply chat', () => {
  const task = { taskId: 't9', goal: 'Apply to Acme', status: 'running', kind: 'application-apply' }

  it('hides the Org control and stage and the access control, and shows the confined chip', async () => {
    await open(coding, task)
    expect(screen.getByTestId('chip-confined')).toHaveTextContent('confined: browser only')
    expect(screen.queryByTestId('chip-access')).toBeNull()
    expect(screen.queryByRole('button', { name: /^Org$/ })).toBeNull()
    expect(document.querySelector('[data-testid="stage-node"]')).toBeNull()
    fireEvent.click(gear())
    expect(within(popover()).queryByLabelText('Access')).toBeNull()
    expect(screen.getByTestId('pane-access-confined')).toHaveTextContent('Confined to the browser')
    expect(within(popover()).queryByRole('switch')).toBeNull()
  })

  it('reads the apply browser level, not coder, with its own Settings link', async () => {
    api.browserPermissions.mockResolvedValue({
      kinds: [...kinds.filter(k => k.kind !== 'apply'), { kind: 'apply', label: 'Apply', level: 'ask', isDefault: true }], catalog: [{ id: 'ask', label: 'Ask first' }, { id: 'dev', label: 'Developer' }],
    })
    await open(coding, task)
    fireEvent.click(gear())
    await waitFor(() => expect(within(screen.getByTestId('pane-browser')).getByText('Ask first')).toBeInTheDocument())
    expect(within(screen.getByTestId('pane-browser')).queryByText('Developer')).toBeNull()
    expect(within(screen.getByTestId('pane-browser')).getByRole('button', { name: 'Browser access (apply) in Settings' })).toBeInTheDocument()
  })

  it('is confined the moment it opens from the button, with no stored state', async () => {
    await open({ id: 'k7' }, { taskId: 't7', kind: 'application-apply', goal: 'g', status: 'running' })
    expect(await screen.findByTestId('chip-confined')).toBeInTheDocument()
    expect(store.bubbles[0].taskKind).toBe('application-apply')
  })

  it('stays confined after a restart, from the stored task kind', async () => {
    await open(coding, task)
    await waitFor(() => expect(localStorage.getItem('monoagent:coderBubbles:v1')).toBeTruthy())
    const saved = localStorage.getItem('monoagent:coderBubbles:v1')
    cleanup()
    localStorage.setItem('monoagent:coderBubbles:v1', saved)
    render(<Harness conv={{ id: 'other' }} />)
    // The restored bubble is the first one; open it.
    await waitFor(() => expect(document.querySelector('[data-bubble="k1"]')).toBeInTheDocument())
    fireEvent.click(document.querySelector('[data-bubble="k1"]'))
    await waitFor(() => expect(screen.getByTestId('chip-confined')).toBeInTheDocument())
  })
})
