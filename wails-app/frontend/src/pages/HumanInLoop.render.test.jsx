// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
import HumanInLoop, { sortBySuggestion } from './HumanInLoop.jsx'
import * as WailsApp from '../wailsjs/go/main/App'
import { api } from '../services/api.js'

vi.mock('../wailsjs/go/main/App', () => ({
  GetHILItems: vi.fn(),
  ApproveHIL: vi.fn(),
  RejectHIL: vi.fn(),
  GetDraftPersonMessages: vi.fn(),
  SendDraftPersonMessage: vi.fn(),
  RejectDraftPersonMessage: vi.fn(),
  GetPendingPeopleApprovals: vi.fn(),
  ApprovePendingPerson: vi.fn(),
  RejectPendingPerson: vi.fn(),
}))

vi.mock('../services/api.js', () => ({
  api: {
    getPendingPeopleApprovals: vi.fn(),
    approvePendingPerson: vi.fn(),
    rejectPendingPerson: vi.fn(),
    listOrgDesigns: vi.fn(),
    getOrgQuestions: vi.fn(),
    getOrgApprovals: vi.fn(),
    getOrgGates: vi.fn(),
    answerOrgQuestion: vi.fn(),
    approveOrgAction: vi.fn(),
    denyOrgAction: vi.fn(),
    gateApproveOrgAction: vi.fn(),
    gateRejectOrgAction: vi.fn(),
  },
  notify: vi.fn(),
}))

vi.mock('../lib/usePageVisible.js', () => ({
  usePageVisibleRef: () => ({ current: true }),
  useVisibleCatchUp: () => {},
}))

describe('HumanInLoop unified approvals', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  afterEach(() => {
    cleanup()
  })

  it('renders lead approvals and triggers approve & send', async () => {
    WailsApp.GetHILItems.mockResolvedValue([])
    WailsApp.GetDraftPersonMessages.mockResolvedValue([])
    api.getPendingPeopleApprovals.mockResolvedValue([
      {
        id: 'lead-1',
        platform: 'LINKEDIN',
        platform_username: 'yashsagar',
        full_name: 'Yash Sagar Santani',
        job_title: 'Full Stack Engineer @ XcelPros',
        introduction: 'Hi Yash, congrats on launching CompX!',
        created_at: '2026-09-24 18:00:00',
      },
    ])
    api.listOrgDesigns.mockResolvedValue({ items: [] })

    const onCountChange = vi.fn()

    render(
      <HumanInLoop
        embedded
        isOpen={true}
        onClose={() => {}}
        onPendingCountChange={onCountChange}
      />
    )

    await waitFor(() => {
      expect(screen.getByText('Yash Sagar Santani')).toBeInTheDocument()
      expect(screen.getByText('@yashsagar')).toBeInTheDocument()
    })

    expect(screen.getByText('Hi Yash, congrats on launching CompX!')).toBeInTheDocument()
    expect(onCountChange).toHaveBeenCalledWith(1)

    // Click Approve & Send
    const approveSendBtn = screen.getByTitle('Save changes, approve, and trigger outreach dispatch workflow')
    expect(approveSendBtn).toBeInTheDocument()

    api.approvePendingPerson.mockResolvedValue({})
    fireEvent.click(approveSendBtn)

    await waitFor(() => {
      expect(api.approvePendingPerson).toHaveBeenCalledWith(
        'lead-1',
        'Hi Yash, congrats on launching CompX!',
        true
      )
    })
  })

  it('shows a browser pairing card with its profile preselected and sends the choice', async () => {
    WailsApp.GetHILItems.mockResolvedValue([{
      id: 'pair-1', node_name: 'Pair browser: Edge', workflow_name: '', execution_id: 'extpair:abc', status: 'pending',
      readonly_data: {
        browser: 'Edge',
        summary: 'The profiles field lists the profiles this browser may run. Type any to let it run every profile. Left empty, this browser can\'t serve any profile yet.',
      },
      editable_data: { code: '', profiles: 'Work' },
      node_config: { kind: 'extension.pair', claim: 'abc' },
      created_at: '2026-10-28 10:00:00',
    }])
    WailsApp.GetDraftPersonMessages.mockResolvedValue([])
    WailsApp.ApproveHIL.mockResolvedValue(undefined)
    api.getPendingPeopleApprovals.mockResolvedValue([])
    api.listOrgDesigns.mockResolvedValue({ items: [] })

    render(<HumanInLoop embedded isOpen={true} onClose={() => {}} />)

    const profiles = await screen.findByDisplayValue('Work')
    expect(screen.getByText(/can't serve any profile yet/)).toBeInTheDocument()
    fireEvent.change(profiles, { target: { value: 'any' } })
    fireEvent.change(screen.getByDisplayValue(''), { target: { value: 'ABC-123' } })
    fireEvent.click(screen.getByText('Approve & Continue'))

    await waitFor(() => expect(WailsApp.ApproveHIL).toHaveBeenCalled())
    const [id, json] = WailsApp.ApproveHIL.mock.calls[0]
    expect(id).toBe('pair-1')
    expect(JSON.parse(json)).toEqual({ code: 'ABC-123', profiles: 'any' })
  })

  it('shows Jev suggestion chips and sorts workflow items by confidence', async () => {
    const hil = (id, name, suggestion) => ({
      id, node_name: name, workflow_name: '', execution_id: 'exec-' + id, status: 'pending',
      readonly_data: {}, editable_data: { caption: 'text ' + id },
      node_config: suggestion ? { suggestion } : {},
      created_at: '2026-09-25 10:00:00',
    })
    WailsApp.GetHILItems.mockResolvedValue([
      hil('h1', 'Plain step', null),
      hil('h2', 'Unsure step', { choice: 'needs_human', p: 0.61, risk: 'medium' }),
      hil('h3', 'Sure step', { choice: 'approve', p: 0.97, risk: 'low' }),
    ])
    WailsApp.GetDraftPersonMessages.mockResolvedValue([])
    api.getPendingPeopleApprovals.mockResolvedValue([
      {
        id: 'lead-1', platform: 'LINKEDIN', platform_username: 'sam', full_name: 'Sam Lee',
        introduction: 'Hi Sam', created_at: '2026-09-24 18:00:00',
        suggestion: { suggest: 'reject', p: 0.88, intro_fit: 'generic', intro_fit_p: 0.8 },
      },
    ])
    api.listOrgDesigns.mockResolvedValue({ items: [] })

    render(<HumanInLoop embedded isOpen={true} onClose={() => {}} />)

    await waitFor(() => {
      expect(screen.getByText('Sure step')).toBeInTheDocument()
    })
    const chips = screen.getAllByTestId('jev-suggestion').map(el => el.textContent)
    expect(chips).toContain('Suggested: approve 97% · risk low')
    expect(chips).toContain('Suggested: needs human 61% · risk medium')
    expect(chips).toContain('Suggested: reject 88% · intro generic')

    // Workflow cards: most confident first, unsuggested last.
    const names = ['Sure step', 'Unsure step', 'Plain step']
    const pos = names.map(n => screen.getByText(n).compareDocumentPosition.bind(screen.getByText(n)))
    expect(pos[0](screen.getByText('Unsure step')) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(pos[1](screen.getByText('Plain step')) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('sortBySuggestion keeps unsuggested items in their order', () => {
    const a = { id: 'a' }, b = { id: 'b', node_config: { suggestion: { choice: 'approve', p: 0.5 } } }, c = { id: 'c' }
    expect(sortBySuggestion([a, b, c]).map(x => x.id)).toEqual(['b', 'a', 'c'])
  })
})
