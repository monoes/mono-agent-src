// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, waitFor, cleanup, within } from '@testing-library/react'
import i18n from '../i18n.js'
import HumanInLoop from './HumanInLoop.jsx'
import { AutoDecideSkipped, skipText } from '../components/hil/AutoDecideSkipped.jsx'
import * as WailsApp from '../wailsjs/go/main/App'
import { api } from '../services/api.js'

// A workflow item whose auto_decide did not decide it says why on its card
// (node_config.auto_decide_skipped), legibly for the two reasons a decision
// provider gives: it did not answer (an error class, never its text), or it
// is on another host and the run is one an AI started or wrote.

vi.mock('../wailsjs/go/main/App', () => ({
  GetHILItems: vi.fn(), ApproveHIL: vi.fn(), RejectHIL: vi.fn(),
  GetDraftPersonMessages: vi.fn(), SendDraftPersonMessage: vi.fn(), RejectDraftPersonMessage: vi.fn(),
  GetPendingPeopleApprovals: vi.fn(), ApprovePendingPerson: vi.fn(), RejectPendingPerson: vi.fn(),
}))
vi.mock('../services/api.js', () => ({
  api: {
    getPendingPeopleApprovals: vi.fn(), approvePendingPerson: vi.fn(), rejectPendingPerson: vi.fn(),
    listOrgPending: vi.fn(),
    answerOrgQuestion: vi.fn(), approveOrgAction: vi.fn(), denyOrgAction: vi.fn(),
    gateApproveOrgAction: vi.fn(), gateRejectOrgAction: vi.fn(),
  },
  notify: vi.fn(),
}))
vi.mock('../lib/usePageVisible.js', () => ({ usePageVisibleRef: () => ({ current: true }), useVisibleCatchUp: () => {} }))

const NO_ANSWER = 'jev: decision provider "local" did not answer (timeout)'
const REMOTE = 'a decision provider on another host is not used from a run an AI started or wrote (an org started this run): ' +
  'provider "router" is at https://openrouter.ai/api/v1; name a provider on this machine or typesafe, or run the workflow yourself'

beforeEach(async () => { vi.clearAllMocks(); await i18n.changeLanguage('en') })
afterEach(() => cleanup())

describe('auto_decide skip reasons', () => {
  it('rewrites the provider reasons and keeps any other as stored', () => {
    const t = i18n.t.bind(i18n)
    expect(skipText(NO_ANSWER, t)).toBe('decision provider local did not answer (timeout).')
    expect(skipText(REMOTE, t)).toBe('provider router is at https://openrouter.ai/api/v1, another host, and a decision provider on another host ' +
      'is not used from a run an AI started or wrote (an org started this run). Name a provider on this computer or typesafe, or run the workflow yourself.')
    const other = 'TypeSafe Jev surface "hil" is not enabled for this profile (monoagentcli jev enable hil)'
    expect(skipText(other, t)).toBe(other)
  })

  it('renders nothing without a reason', () => {
    const { container } = render(<AutoDecideSkipped item={{ node_config: {} }} />)
    expect(container).toBeEmptyDOMElement()
  })

  it('shows the reason on the workflow card', async () => {
    const hil = (id, name, reason) => ({
      id, node_name: name, workflow_name: '', execution_id: 'exec-' + id, status: 'pending',
      readonly_data: {}, editable_data: { caption: 'x' }, node_config: { auto_decide_skipped: reason },
      created_at: '2026-11-22 10:00:00',
    })
    WailsApp.GetHILItems.mockResolvedValue([hil('h1', 'Timed out step', NO_ANSWER), hil('h2', 'Org step', REMOTE)])
    WailsApp.GetDraftPersonMessages.mockResolvedValue([])
    api.getPendingPeopleApprovals.mockResolvedValue([])
    api.listOrgPending.mockResolvedValue({ items: [] })
    render(<HumanInLoop embedded isOpen={true} onClose={() => {}} />)
    await waitFor(() => expect(screen.getAllByTestId('hil-auto-decide-skipped')).toHaveLength(2))
    const [a, b] = screen.getAllByTestId('hil-auto-decide-skipped')
    expect(a).toHaveTextContent('Jev did not decide this item: decision provider local did not answer (timeout).')
    expect(within(b).getByText(/provider router is at https:\/\/openrouter\.ai\/api\/v1, another host/)).toBeInTheDocument()
  })
})
