// @vitest-environment jsdom
// Org items in Human in Loop: they come from `org pending`, an approval shows
// what it would run and in which folder, and it is resolved by its request id.
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup, within } from '@testing-library/react'
import '../i18n.js'
import HumanInLoop from './HumanInLoop.jsx'
import * as WailsApp from '../wailsjs/go/main/App'
import { api, notify } from '../services/api.js'

vi.mock('../wailsjs/go/main/App', () => ({
  GetHILItems: vi.fn(), ApproveHIL: vi.fn(), RejectHIL: vi.fn(),
  GetDraftPersonMessages: vi.fn(), SendDraftPersonMessage: vi.fn(), RejectDraftPersonMessage: vi.fn(),
  GetPendingPeopleApprovals: vi.fn(), ApprovePendingPerson: vi.fn(), RejectPendingPerson: vi.fn(),
}))
vi.mock('../services/api.js', () => ({
  api: {
    getPendingPeopleApprovals: vi.fn(), approvePendingPerson: vi.fn(), rejectPendingPerson: vi.fn(),
    listOrgPending: vi.fn(), answerOrgQuestion: vi.fn(), approveOrgAction: vi.fn(), denyOrgAction: vi.fn(),
    gateApproveOrgAction: vi.fn(), gateRejectOrgAction: vi.fn(),
  },
  notify: vi.fn(),
}))
// One ref for every render, as the real hook gives: a new one each time would
// reload the page on every render and bring a resolved card back.
vi.mock('../lib/usePageVisible.js', () => {
  const ref = { current: true }
  return { usePageVisibleRef: () => ref, useVisibleCatchUp: () => {} }
})

const approval = (ref, action, extra) => ({
  org: 'growth', kind: 'approval', id: ref, ref, role: 'dev', action, text: `Approve ${action} tool call?`, pending: true, request_id: ref, ...extra,
})
// As `monoagentcli org pending` prints them.
const PENDING = {
  v: 1,
  errors: [],
  items: [
    { org: 'growth', kind: 'question', id: 'q-1', ref: 'q-1', role: 'lead', text: 'Which market first?', pending: true },
    approval('apr-1', 'Bash', { command: 'echo safe \u202e# txt.exe', cwd: '/home/u/proj' }),
    approval('apr-2', 'Bash', { command: 'git push --force', command_truncated: true, cwd: '/home/u/proj' }),
    approval('apr-3', 'WebFetch', { input: { url: 'https://example.com/\u202egnp.exe' } }),
    approval('dev:org_complete:7', 'org_complete', { request_id: undefined }),
    { org: 'growth', kind: 'gate', id: 'gate-1', ref: 'gate-1', role: 'lead', name: 'publish', text: 'Publish the post', pending: true },
  ],
}

beforeEach(() => {
  vi.clearAllMocks()
  WailsApp.GetHILItems.mockResolvedValue([])
  WailsApp.GetDraftPersonMessages.mockResolvedValue([])
  api.getPendingPeopleApprovals.mockResolvedValue([])
  api.listOrgPending.mockResolvedValue(PENDING)
  for (const fn of ['answerOrgQuestion', 'approveOrgAction', 'denyOrgAction', 'gateApproveOrgAction', 'gateRejectOrgAction']) {
    api[fn].mockResolvedValue({ v: 1 })
  }
})
afterEach(() => cleanup())

const show = async (props = {}) => {
  render(<HumanInLoop embedded isOpen onClose={() => {}} {...props} />)
  return screen.findAllByTestId('org-decision')
}

describe('Human in Loop org items', () => {
  it('lists what the orgs wait on, with the command and folder of a shell call', async () => {
    const onPendingCountChange = vi.fn()
    const cards = await show({ onPendingCountChange })
    expect(cards).toHaveLength(6)
    expect(onPendingCountChange).toHaveBeenCalledWith(6)
    expect(cards[0]).toHaveTextContent('Which market first?')
    expect(cards[5]).toHaveTextContent('Publish the post')

    const box = within(cards[1]).getByTestId('approval-command')
    expect(box).toHaveTextContent('Role folder: /home/u/proj')
    // The reordering character is written out, not obeyed.
    expect(within(box).getByTestId('approval-escape')).toHaveTextContent('\\u202E')
    expect(box.textContent).not.toContain('\u202e')
    expect(within(cards[2]).getByTestId('approval-command')).toHaveTextContent('git push --force')

    // A call that is no shell command shows its arguments, escaped the same way.
    const input = within(cards[3]).getByTestId('approval-input')
    expect(input).toHaveTextContent('https://example.com/')
    expect(within(input).getByTestId('approval-escape')).toHaveTextContent('\\u202E')
    expect(input.textContent).not.toContain('\u202e')
  })

  // Approve releases the whole call, so the card hides none of its arguments.
  it('shows every other argument of a call that has a command', async () => {
    api.listOrgPending.mockResolvedValue({ v: 1, errors: [], items: [
      approval('apr-9', 'Bash', { command: 'make deploy', input: { command: 'make deploy', dangerouslyDisableSandbox: true } }),
    ] })
    const [card] = await show()
    expect(within(card).getByTestId('approval-command')).toHaveTextContent('make deploy')
    expect(within(card).getByTestId('approval-input')).toHaveTextContent('"dangerouslyDisableSandbox": true')
  })

  it('approves and denies only the request on the card, by id', async () => {
    const cards = await show()
    fireEvent.click(within(cards[1]).getByRole('button', { name: /Approve/ }))
    await waitFor(() => expect(screen.getAllByTestId('org-decision')).toHaveLength(5))
    expect(api.approveOrgAction.mock.calls).toEqual([['growth', 'dev', 'Bash', 'apr-1']])
    // The other Bash call of the same role stays.
    expect(screen.getByText('git push --force')).toBeInTheDocument()
    fireEvent.click(within(screen.getAllByTestId('org-decision')[1]).getByRole('button', { name: /Deny/ }))
    await waitFor(() => expect(api.denyOrgAction.mock.calls).toEqual([['growth', 'dev', 'Bash', 'apr-2']]))
    await waitFor(() => expect(screen.getAllByTestId('org-decision')).toHaveLength(4))
  })

  it('keeps the card and says why when the CLI refuses', async () => {
    api.approveOrgAction.mockResolvedValue({ error: 'no pending approval apr-1 found' })
    const cards = await show()
    fireEvent.click(within(cards[1]).getByRole('button', { name: /Approve/ }))
    await waitFor(() => expect(notify).toHaveBeenCalledWith('org', 'no pending approval apr-1 found'))
    expect(screen.getByText('no pending approval apr-1 found')).toBeInTheDocument()
    expect(screen.getAllByTestId('org-decision')).toHaveLength(6)
    expect(notify).not.toHaveBeenCalledWith('org', 'Approved Bash')
  })

  it('offers no Approve or Deny for a request that has no id', async () => {
    const cards = await show()
    expect(within(cards[4]).queryByRole('button', { name: /Approve|Deny/ })).not.toBeInTheDocument()
    expect(within(cards[4]).getByText(/has no request id/)).toBeInTheDocument()
  })

  it('answers a question and resolves a gate', async () => {
    const cards = await show()
    fireEvent.change(within(cards[0]).getByPlaceholderText('Type your answer…'), { target: { value: 'EU first' } })
    fireEvent.click(within(cards[0]).getByRole('button', { name: 'Submit Answer' }))
    await waitFor(() => expect(api.answerOrgQuestion).toHaveBeenCalledWith('growth', 'q-1', 'EU first'))
    fireEvent.click(within(cards[5]).getByRole('button', { name: /Approve Gate/ }))
    await waitFor(() => expect(api.gateApproveOrgAction).toHaveBeenCalledWith('growth', 'gate-1'))
  })

  it('reports an org that could not be read', async () => {
    api.listOrgPending.mockResolvedValue({ v: 1, items: [], errors: [{ org: 'ops', error: 'questions: not valid JSON' }] })
    render(<HumanInLoop embedded isOpen onClose={() => {}} />)
    expect(await screen.findByText(/Org ops: questions: not valid JSON/)).toBeInTheDocument()
    expect(screen.queryByText('All caught up!')).not.toBeInTheDocument()
  })
})
