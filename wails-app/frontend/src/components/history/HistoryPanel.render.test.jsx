// @vitest-environment jsdom
import { readFileSync } from 'node:fs'
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup, act, within } from '@testing-library/react'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key, o) => (o && o.names != null && key === 'history.keptRunning' ? `${key}:${o.count}:${o.names}` : o && o.count != null ? `${key}:${o.count}` : o && o.title ? `${key}:${o.title}` : key) }),
}))

const { listHistory, deleteChats, handlers, confirmMock, getOrgStatus } = vi.hoisted(() => ({
  listHistory: vi.fn(), deleteChats: vi.fn(), handlers: {}, confirmMock: vi.fn(), getOrgStatus: vi.fn(),
}))
vi.mock('../../services/api.js', () => ({
  api: { listHistory, deleteChatConversations: deleteChats, getOrgStatus },
  subscribeEvent: (name, cb) => { handlers[name] = cb; return () => { delete handlers[name] } },
}))
vi.mock('../ConfirmDialog.jsx', () => ({ confirm: confirmMock }))

import HistoryPanel from './HistoryPanel.jsx'
import { openHistory } from './historyBus.js'
import { CoderBubbleContext } from '../bubbles/CoderBubbleContext.jsx'

const chat = (id, extra = {}) => ({ id, kind: 'chat', title: `Chat ${id}`, runtime: 'claude', model: 'opus', access: 'coding', status: 'done', updatedAt: new Date().toISOString(), ...extra })
const org = (id, extra = {}) => ({ id, kind: 'org', title: `Run ${id}`, orgName: 'acme', status: 'running', updatedAt: new Date().toISOString(), ...extra })
const page = (items, extra = {}) => ({ items, next_cursor: '', warnings: [], ...extra })

function mount(store = {}, ctx = {}) {
  const s = { bubbles: [], summaryOf: () => null, openConversation: vi.fn(), openOrg: vi.fn(), ...store }
  const onNavigate = vi.fn()
  render(
    <CoderBubbleContext.Provider value={{ openDraft: vi.fn(), navigate: null, ...ctx }}>
      <button>opener</button>
      <HistoryPanel store={s} onNavigate={onNavigate} />
    </CoderBubbleContext.Provider>,
  )
  return { s, onNavigate }
}
const open = async () => { await act(async () => { openHistory() }) }

beforeEach(() => {
  vi.clearAllMocks()
  getOrgStatus.mockResolvedValue({ status: 'running', run: 'r-live' })
  for (const k of Object.keys(handlers)) delete handlers[k]
  listHistory.mockResolvedValue(page([chat('c1'), org('r1'), chat('c2', { applicationId: 'app9', taskGoal: 'Fill OmniCorp form', access: 'full', status: 'needs' })]))
})
afterEach(() => { cleanup(); vi.useRealTimers() })

describe('HistoryPanel', () => {
  it('renders nothing until opened, then a labelled non-modal dialog with rows', async () => {
    mount()
    expect(screen.queryByRole('dialog')).toBeNull()
    await open()
    const dlg = await screen.findByRole('dialog', { name: 'history.title' })
    expect(dlg).toHaveAttribute('aria-modal', 'false')
    const list = await screen.findByRole('list', { name: 'history.list' })
    expect(within(list).getAllByRole('listitem')).toHaveLength(3)
    expect(screen.getByText('Fill OmniCorp form')).toBeInTheDocument()
    expect(screen.getByText('history.access_full')).toBeInTheDocument()
  })

  it('opens a chat as a bubble and an org run as its org', async () => {
    const { s } = mount()
    await open()
    fireEvent.click(await screen.findByText('Chat c1'))
    // the complete row reaches the store (access, mode, title, task...)
    expect(s.openConversation).toHaveBeenCalledWith(expect.objectContaining({ id: 'c1', kind: 'chat', title: 'Chat c1', access: 'coding', runtime: 'claude', model: 'opus', status: 'done' }))
    await open()
    fireEvent.click(await screen.findByText('Run r1'))
    await waitFor(() => expect(s.openOrg).toHaveBeenCalledWith('acme', { runId: 'r-live', finished: false }))
  })

  it('opens the running run of an org as its live bubble', async () => {
    listHistory.mockResolvedValue(page([org('r9', { runId: 'r-live', status: 'running' })]))
    const { s, onNavigate } = mount()
    await open()
    fireEvent.click(await screen.findByText('Run r9'))
    await waitFor(() => expect(s.openOrg).toHaveBeenCalledWith('acme', { runId: 'r-live', finished: false }))
    expect(onNavigate).not.toHaveBeenCalled()
  })

  it('opens an older run of an org as a read-only bubble for that run', async () => {
    listHistory.mockResolvedValue(page([org('r8', { runId: 'r-old-12345', status: 'done', updatedAt: '2026-10-20T10:00:00Z' })]))
    const { s, onNavigate } = mount()
    await open()
    expect(await screen.findByText('run r-old-12 \u00b7 2026-10-20')).toBeInTheDocument()
    fireEvent.click(screen.getByText('Run r8'))
    await waitFor(() => expect(s.openOrg).toHaveBeenCalledWith('acme', { runId: 'r-old-12345', finished: true }))
    expect(onNavigate).not.toHaveBeenCalled()
  })

  it('keeps Open in Orgs as a secondary action on an org-run row', async () => {
    listHistory.mockResolvedValue(page([org('r8', { runId: 'r-old-12345', status: 'done' })]))
    const { s, onNavigate } = mount()
    await open()
    fireEvent.click(await screen.findByRole('button', { name: 'history.openInOrgs' }))
    expect(onNavigate).toHaveBeenCalledWith('orgs', { org: 'acme', run: 'r-old-12345' })
    expect(s.openOrg).not.toHaveBeenCalled()
  })

  it('goes to the application from a linked row', async () => {
    const { onNavigate } = mount()
    await open()
    fireEvent.click(await screen.findByRole('button', { name: 'history.openApplication' }))
    expect(onNavigate).toHaveBeenCalledWith('applications', { application: 'app9' })
  })

  it('overlays an open bubble live status and unread count', async () => {
    mount({ bubbles: [{ key: 'c1' }], summaryOf: () => ({ status: 'working', unread: 3 }) })
    await open()
    const row = (await screen.findByText('Chat c1')).closest('li')
    expect(row).toHaveAttribute('data-status', 'running')
    expect(within(row).getByLabelText('history.unread:3')).toBeInTheDocument()
  })

  it('debounces search into filter.query and passes kind/status chips', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    mount()
    await open()
    await screen.findByText('Chat c1')
    fireEvent.change(screen.getByRole('searchbox', { name: 'history.search' }), { target: { value: 'form' } })
    expect(listHistory).not.toHaveBeenCalledWith('', expect.anything(), expect.objectContaining({ query: 'form' }))
    await act(async () => { vi.advanceTimersByTime(350) })
    await waitFor(() => expect(listHistory).toHaveBeenLastCalledWith('', 30, expect.objectContaining({ query: 'form' })))
    fireEvent.click(screen.getByRole('button', { name: 'history.kindOrg' }))
    await waitFor(() => expect(listHistory).toHaveBeenLastCalledWith('', 30, expect.objectContaining({ kind: 'org' })))
    fireEvent.change(screen.getByRole('combobox', { name: 'history.statusLabel' }), { target: { value: 'error' } })
    await waitFor(() => expect(listHistory).toHaveBeenLastCalledWith('', 30, expect.objectContaining({ status: 'error' })))
  })

  it('the linked chip asks the CLI for any application link', async () => {
    mount()
    await open()
    await screen.findByText('Chat c1')
    fireEvent.click(screen.getByRole('button', { name: 'history.linked' }))
    await waitFor(() => expect(listHistory).toHaveBeenLastCalledWith('', 30, expect.objectContaining({ application: 'any' })))
  })

  it('loads the next page with next_cursor', async () => {
    listHistory.mockResolvedValueOnce(page([chat('c1')], { next_cursor: 'k2' })).mockResolvedValueOnce(page([chat('c9')]))
    mount()
    await open()
    fireEvent.click(await screen.findByRole('button', { name: 'history.loadMore' }))
    expect(await screen.findByText('Chat c9')).toBeInTheDocument()
    expect(listHistory).toHaveBeenLastCalledWith('k2', 30, expect.anything())
  })

  it('shows warnings as a quiet banner above a still-usable list', async () => {
    listHistory.mockResolvedValue(page([chat('c1')], { warnings: ['beta', 'gamma'] }))
    mount()
    await open()
    expect(await screen.findByText('history.warning:2')).toBeInTheDocument()
    expect(screen.getByText('Chat c1')).toBeInTheDocument()
  })

  it('refreshes on a finished turn and an org status change, at most once per 2s', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    mount()
    await open()
    await screen.findByText('Chat c1')
    listHistory.mockClear()
    await act(async () => { vi.advanceTimersByTime(2100) })
    await act(async () => { handlers['chat:event']({ type: 'turn.started' }) })
    expect(listHistory).not.toHaveBeenCalled()
    await act(async () => { handlers['chat:event']({ type: 'turn.finished' }); handlers['org:runStatus']({ status: 'done' }); handlers['chat:event']({ type: 'turn.finished' }) })
    expect(listHistory).toHaveBeenCalledTimes(1)
    await act(async () => { vi.advanceTimersByTime(2100) })
    expect(listHistory).toHaveBeenCalledTimes(2)
  })

  it('unsubscribes when closed', async () => {
    mount()
    await open()
    await screen.findByText('Chat c1')
    expect(handlers['chat:event']).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: 'history.close' }))
    expect(handlers['chat:event']).toBeUndefined()
    expect(handlers['org:runStatus']).toBeUndefined()
  })

  it('shows the empty state with a New chat button', async () => {
    listHistory.mockResolvedValue(page([]))
    const openDraft = vi.fn()
    mount({}, { openDraft })
    await open()
    expect(await screen.findByText('history.empty')).toBeInTheDocument()
    const btns = screen.getAllByRole('button', { name: 'history.newChat' })
    fireEvent.click(btns[btns.length - 1])
    expect(openDraft).toHaveBeenCalled()
  })

  it('shows the error with the binding text verbatim in details, and retries', async () => {
    listHistory.mockRejectedValueOnce(new Error('monoagentcli: not found')).mockResolvedValueOnce(page([chat('c1')]))
    mount()
    await open()
    expect(await screen.findByRole('alert')).toHaveTextContent('history.error')
    expect(screen.getByText('monoagentcli: not found').closest('details')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'history.retry' }))
    expect(await screen.findByText('Chat c1')).toBeInTheDocument()
  })

  it('shows a loading skeleton while the first page loads', async () => {
    listHistory.mockReturnValue(new Promise(() => {}))
    mount()
    await open()
    expect(screen.getByLabelText('history.loading')).toHaveAttribute('aria-busy', 'true')
  })

  it('moves focus in on open, closes on Escape and returns focus to the opener', async () => {
    mount()
    const opener = screen.getByText('opener')
    opener.focus()
    await open()
    await screen.findByText('Chat c1')
    expect(screen.getByRole('searchbox')).toHaveFocus()
    vi.useFakeTimers({ shouldAdvanceTime: true })
    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' })
    expect(screen.queryByRole('dialog')).toBeNull()
    await act(async () => { vi.advanceTimersByTime(20) })
    expect(opener).toHaveFocus()
  })

  it('returns focus to the status-bar toggle when the opener was <body> (WebKit does not focus a clicked button)', async () => {
    mount()
    const toggle = document.createElement('button'); toggle.setAttribute('data-testid', 'history-status-toggle'); document.body.appendChild(toggle)
    document.activeElement.blur()
    expect(document.activeElement).toBe(document.body)
    await open()
    await screen.findByText('Chat c1')
    vi.useFakeTimers({ shouldAdvanceTime: true })
    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' })
    await act(async () => { vi.advanceTimersByTime(20) })
    expect(toggle).toHaveFocus()
    toggle.remove()
  })

  it('draws above the bubble dock and overlay (9600-9602) and below confirm dialogs (10001)', () => {
    const css = readFileSync('src/components/history/history.css', 'utf8')
    const z = Number(/\.hist-drawer \{[^}]*z-index:\s*(\d+)/.exec(css)[1])
    expect(z).toBeGreaterThan(9602)
    expect(z).toBeLessThan(10001)
  })

  it('Escape closes the drawer when focus fell to the body, but not under a modal', async () => {
    mount()
    await open()
    await screen.findByText('Chat c1')
    document.activeElement.blur()
    const modal = document.createElement('div'); modal.setAttribute('aria-modal', 'true'); document.body.appendChild(modal)
    fireEvent.keyDown(document.body, { key: 'Escape' })
    expect(screen.getByRole('dialog', { name: 'history.title' })).toBeInTheDocument()
    modal.remove()
    fireEvent.keyDown(document.body, { key: 'Escape' })
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  describe('refresh reconciliation', () => {
    const at = n => new Date(Date.UTC(2026, 9, 22, 12, 0, 0) - n * 60000).toISOString()

    it('drops a row from a status filter once its turn finished', async () => {
      vi.useFakeTimers({ shouldAdvanceTime: true })
      const running = [chat('c1', { status: 'running', updatedAt: at(1) }), chat('c2', { status: 'running', updatedAt: at(2) })]
      listHistory.mockResolvedValue(page(running))
      mount()
      await open()
      fireEvent.change(await screen.findByRole('combobox', { name: 'history.statusLabel' }), { target: { value: 'running' } })
      await waitFor(() => expect(listHistory).toHaveBeenLastCalledWith('', 30, expect.objectContaining({ status: 'running' })))
      await screen.findByText('Chat c1')
      await act(async () => { vi.advanceTimersByTime(2100) })
      listHistory.mockResolvedValue(page([running[1]]))
      await act(async () => { handlers['chat:event']({ type: 'turn.finished' }) })
      await waitFor(() => expect(screen.queryByText('Chat c1')).toBeNull())
      expect(screen.getByText('Chat c2')).toBeInTheDocument()
    })

    it('keeps rows beyond the first page through a refresh', async () => {
      vi.useFakeTimers({ shouldAdvanceTime: true })
      const p1 = [chat('c1', { updatedAt: at(1) }), chat('c2', { updatedAt: at(2) })]
      listHistory.mockResolvedValueOnce(page(p1, { next_cursor: 'k2' })).mockResolvedValueOnce(page([chat('c3', { updatedAt: at(3) })]))
      mount()
      await open()
      fireEvent.click(await screen.findByRole('button', { name: 'history.loadMore' }))
      await screen.findByText('Chat c3')
      await act(async () => { vi.advanceTimersByTime(2100) })
      listHistory.mockResolvedValue(page([chat('c0', { updatedAt: at(0) }), p1[1]], { next_cursor: 'k9' }))
      await act(async () => { handlers['chat:event']({ type: 'turn.finished' }) })
      await screen.findByText('Chat c0')
      expect(screen.getByText('Chat c3')).toBeInTheDocument() // beyond the boundary: kept
      expect(screen.queryByText('Chat c1')).toBeNull() // newer than the boundary and missing: gone
    })

    it('a refresh already in flight cannot bring a deleted row back', async () => {
      vi.useFakeTimers({ shouldAdvanceTime: true })
      confirmMock.mockResolvedValue(true)
      mount()
      await open()
      await screen.findByText('Chat c1')
      await act(async () => { vi.advanceTimersByTime(2100) })
      let release
      const stale = page([chat('c1'), org('r1'), chat('c2', { applicationId: 'app9', status: 'needs' })])
      listHistory.mockImplementationOnce(() => new Promise(r => { release = () => r(stale) }))
      await act(async () => { handlers['chat:event']({ type: 'turn.finished' }) })
      deleteChats.mockResolvedValue({ deleted: ['c1'], active: [], failed: {} })
      fireEvent.click(screen.getByRole('checkbox', { name: 'history.select:Chat c1' }))
      fireEvent.click(screen.getByRole('button', { name: 'history.deleteSelected' }))
      await waitFor(() => expect(deleteChats).toHaveBeenCalled())
      await waitFor(() => expect(screen.queryByText('Chat c1')).toBeNull())
      await act(async () => { release() })
      expect(screen.queryByText('Chat c1')).toBeNull()
    })
  })

  describe('delete', () => {
    it('confirms, deletes chats only, and explains a still-active row', async () => {
      confirmMock.mockResolvedValue(true)
      deleteChats.mockImplementation(async () => {
        listHistory.mockResolvedValue(page([org('r1'), chat('c2')])) // what the server lists after c1 is gone
        return { deleted: ['c1'], active: ['c2'], failed: {} }
      })
      mount()
      await open()
      fireEvent.click(await screen.findByRole('checkbox', { name: 'history.select:Chat c1' }))
      fireEvent.click(screen.getByRole('checkbox', { name: 'history.select:Chat c2' }))
      // org rows offer no checkbox
      expect(screen.queryByRole('checkbox', { name: /Run r1/ })).toBeNull()
      fireEvent.click(screen.getByRole('button', { name: 'history.deleteSelected' }))
      await waitFor(() => expect(deleteChats).toHaveBeenCalledWith(['c1', 'c2']))
      expect(confirmMock).toHaveBeenCalledWith('history.confirmDeleteBody:2', expect.objectContaining({ danger: true }))
      expect(await screen.findByText(/history.stillActive:1/)).toBeInTheDocument()
      expect(screen.queryByText('Chat c1')).toBeNull()
      expect(screen.getByText('Chat c2')).toBeInTheDocument()
    })

    it('names the processes a delete could not stop, and says who may delete a chat that leads a mission', async () => {
      confirmMock.mockResolvedValue(true)
      deleteChats.mockResolvedValue({
        deleted: [], active: [], failed: {},
        kept: {
          c1: { code: 'survivors_remain', message: 'not deleted', stopped: [], survivors: [{ pid: 4242, command: 'node server.js', reason: 'it outlived SIGKILL' }, { pid: 4243, command: 'sleep 600', reason: 'x' }] },
          c2: { code: 'operator_only', message: 'only the operator', stopped: [], survivors: [] },
        },
      })
      mount()
      await open()
      fireEvent.click(await screen.findByRole('checkbox', { name: 'history.select:Chat c1' }))
      fireEvent.click(screen.getByRole('checkbox', { name: 'history.select:Chat c2' }))
      fireEvent.click(screen.getByRole('button', { name: 'history.deleteSelected' }))
      // two processes, named by command and pid; one chat that needs the operator
      expect(await screen.findByText(/history.keptRunning:2:node server.js \(4242\), sleep 600 \(4243\)/)).toBeInTheDocument()
      expect(screen.getByText(/history.keptOperatorOnly:1/)).toBeInTheDocument()
      // nothing was removed from the list: both chats are still there
      expect(screen.getByText('Chat c1')).toBeInTheDocument()
      expect(screen.getByText('Chat c2')).toBeInTheDocument()
    })

    it('does nothing when the confirm is declined', async () => {
      confirmMock.mockResolvedValue(false)
      mount()
      await open()
      fireEvent.click(await screen.findByRole('checkbox', { name: 'history.select:Chat c1' }))
      fireEvent.click(screen.getByRole('button', { name: 'history.deleteSelected' }))
      await waitFor(() => expect(confirmMock).toHaveBeenCalled())
      expect(deleteChats).not.toHaveBeenCalled()
    })

    it('Clear all chats lists chats only, confirms the count and never sends an org run', async () => {
      confirmMock.mockResolvedValue(true)
      listHistory.mockImplementation(async (cursor, limit, f) => {
        if (f.kind === 'chat') return page([chat('c1'), org('r1'), chat('c2')]) // a server slip: org row in a chat page
        return page([chat('c1'), org('r1')])
      })
      deleteChats.mockResolvedValue({ deleted: ['c1', 'c2'], active: [], failed: {} })
      mount()
      await open()
      await screen.findByText('Chat c1')
      fireEvent.click(screen.getByRole('button', { name: 'history.clearAll' }))
      await waitFor(() => expect(deleteChats).toHaveBeenCalledWith(['c1', 'c2']))
      expect(listHistory).toHaveBeenCalledWith('', 100, expect.objectContaining({ kind: 'chat' }))
      expect(confirmMock).toHaveBeenCalledWith('history.clearAllBody:2', expect.anything())
    })

    it('Clear all chats declined deletes nothing', async () => {
      confirmMock.mockResolvedValue(false)
      mount()
      await open()
      await screen.findByText('Chat c1')
      fireEvent.click(screen.getByRole('button', { name: 'history.clearAll' }))
      await waitFor(() => expect(confirmMock).toHaveBeenCalled())
      expect(deleteChats).not.toHaveBeenCalled()
    })
  })
})
