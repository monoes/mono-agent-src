// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import '../../i18n.js'

const { api } = vi.hoisted(() => ({ api: { getOrgProposal: vi.fn(), confirmOrgProposal: vi.fn(), declineOrgProposal: vi.fn() } }))
vi.mock('../../services/api.js', () => ({ api, notify: vi.fn() }))
import OrgProposalCard, { proposalRefs } from './OrgProposalCard.jsx'

const ID = '0123456789abcdef0123456789abcdef'
const HASH = 'b'.repeat(64)
const future = () => new Date(Date.now() + 20 * 60000).toISOString()
const row = (over = {}) => ({
  id: ID, org: 'acme', conversation_id: 'conv-1', verb: 'grant.add', status: 'pending', current: true, hash: HASH,
  preview_text: 'Give role lead the automation publish_post (run, approval required)', created_by: 'chat', expires_at: future(), ...over,
})

beforeEach(() => {
  vi.clearAllMocks()
  api.getOrgProposal.mockResolvedValue({ v: 1, proposal: row() })
})
afterEach(cleanup)

const mount = (props = {}) => render(<OrgProposalCard tool="set_org_grant" id={ID} {...props} />)

describe('OrgProposalCard', () => {
  it('shows the stored preview, and Confirm sends the hash the card displayed', async () => {
    api.confirmOrgProposal.mockResolvedValue({ v: 1, proposal: row({ status: 'applied' }), result: { granted: true } })
    mount()
    expect(await screen.findByTestId('org-proposal-preview')).toHaveTextContent('Give role lead the automation publish_post (run, approval required)')
    expect(screen.getByTestId('org-proposal-hash')).toHaveTextContent(HASH.slice(0, 12))
    expect(api.getOrgProposal).toHaveBeenCalledWith(ID)
    // The row is rewritten after render: Confirm must still send the hash that was displayed.
    api.getOrgProposal.mockResolvedValue({ v: 1, proposal: row({ status: 'applied', hash: 'c'.repeat(64) }) })
    fireEvent.click(screen.getByTestId('org-proposal-confirm'))
    await waitFor(() => expect(api.confirmOrgProposal).toHaveBeenCalledWith(ID, HASH))
    expect(await screen.findByTestId('org-proposal-result')).toHaveTextContent('granted')
    expect(screen.queryByTestId('org-proposal-confirm')).toBeNull()
  })

  it('does not take the preview from anything but the stored row', async () => {
    mount()
    await screen.findByTestId('org-proposal-preview')
    expect(screen.getByTestId('org-proposal-preview').textContent).not.toContain('forged')
  })

  it('refuses a verb the tool does not make, and treats the conversation as a label only', async () => {
    api.getOrgProposal.mockResolvedValue({ v: 1, proposal: row({ conversation_id: 'conv-2' }) })
    const other = mount()
    expect(await screen.findByTestId('org-proposal-confirm')).toBeInTheDocument()
    other.unmount()
    for (const [props, over] of [[{ tool: 'set_org_autonomy' }, {}]]) {
      api.getOrgProposal.mockResolvedValue({ v: 1, proposal: row(over) })
      const { unmount } = mount(props)
      expect(await screen.findByTestId('org-proposal-unavailable')).toBeInTheDocument()
      expect(screen.queryByTestId('org-proposal-confirm')).toBeNull()
      expect(screen.queryByTestId('org-proposal-preview')).toBeNull()
      unmount()
    }
  })

  it('shows nothing of a proposal that cannot be read', async () => {
    api.getOrgProposal.mockResolvedValue({ error: 'proposal_not_found' })
    mount()
    expect(await screen.findByTestId('org-proposal-unavailable')).toBeInTheDocument()
    expect(screen.queryByTestId('org-proposal-confirm')).toBeNull()
  })

  it.each([['applied'], ['declined'], ['failed'], ['expired'], ['stale'], ['applying']])('never offers Confirm on a %s row', async (status) => {
    api.getOrgProposal.mockResolvedValue({ v: 1, proposal: row({ status }) })
    mount()
    await screen.findByTestId('org-proposal-preview')
    expect(screen.queryByTestId('org-proposal-confirm')).toBeNull()
    expect(screen.queryByTestId('org-proposal-decline')).toBeNull()
  })

  it('never offers Confirm past the expiry, or when the org changed', async () => {
    api.getOrgProposal.mockResolvedValue({ v: 1, proposal: row({ expires_at: new Date(Date.now() - 1000).toISOString() }) })
    const { unmount } = mount()
    await screen.findByTestId('org-proposal-preview')
    expect(screen.getByTestId('org-proposal')).toHaveAttribute('data-status', 'expired')
    expect(screen.queryByTestId('org-proposal-confirm')).toBeNull()
    unmount()
    api.getOrgProposal.mockResolvedValue({ v: 1, proposal: row({ current: false }) })
    mount()
    await screen.findByTestId('org-proposal-changed')
    expect(screen.queryByTestId('org-proposal-confirm')).toBeNull()
    expect(screen.getByTestId('org-proposal-decline')).toBeInTheDocument()
  })

  it.each([
    ['proposal_stale', /org changed/],
    ['proposal_expired', /expired/],
    ['proposal_hash_mismatch', /no longer matches/],
    ['proposal_already_done', /already decided/],
  ])('shows the CLI refusal %s as plain text and re-reads the row', async (code, text) => {
    api.confirmOrgProposal.mockResolvedValue({ error: code })
    mount()
    fireEvent.click(await screen.findByTestId('org-proposal-confirm'))
    expect(await screen.findByTestId('org-proposal-error')).toHaveTextContent(text)
    await waitFor(() => expect(api.getOrgProposal).toHaveBeenCalledTimes(2))
  })

  it('disables both buttons while a click is in flight, and Decline declines', async () => {
    let finish
    api.declineOrgProposal.mockReturnValue(new Promise(r => { finish = r }))
    mount()
    fireEvent.click(await screen.findByTestId('org-proposal-decline'))
    await waitFor(() => expect(screen.getByTestId('org-proposal-confirm')).toBeDisabled())
    expect(screen.getByTestId('org-proposal-decline')).toBeDisabled()
    api.getOrgProposal.mockResolvedValue({ v: 1, proposal: row({ status: 'declined' }) })
    finish({ v: 1, proposal: row({ status: 'declined' }) })
    await waitFor(() => expect(screen.getByTestId('org-proposal')).toHaveAttribute('data-status', 'declined'))
    expect(api.declineOrgProposal).toHaveBeenCalledWith(ID)
    expect(api.confirmOrgProposal).not.toHaveBeenCalled()
  })
})

describe('proposalRefs', () => {
  const call = (over) => ({ callId: 'c1', name: 'set_org_grant', status: 'completed', ok: true, result: JSON.stringify({ proposal_id: ID }), ...over })
  it('reads the id of a completed grant or autonomy call', () => {
    expect(proposalRefs({ c1: call() })).toEqual([{ key: 'c1', tool: 'set_org_grant', id: ID }])
    expect(proposalRefs({ c1: call({ name: 'mcp__monoagent__set_org_autonomy' }) })[0].tool).toBe('set_org_autonomy')
  })
  it('ignores other tools, running or failed calls, odd ids and odd results', () => {
    expect(proposalRefs({
      a: call({ name: 'create_org' }), b: call({ callId: 'b', status: 'started' }), c: call({ callId: 'c', ok: false }),
      d: call({ callId: 'd', result: JSON.stringify({ proposal_id: '../x' }) }), e: call({ callId: 'e', result: 'not json' }),
      f: call({ callId: 'f', result: null }), g: call({ callId: 'g', result: '[]' }),
    })).toEqual([])
  })
  it('lists a proposal once', () => {
    expect(proposalRefs({ a: call(), b: call({ callId: 'b' }) })).toHaveLength(1)
  })
})
