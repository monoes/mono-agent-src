// @vitest-environment jsdom
import React, { useEffect } from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, waitFor, cleanup, configure } from '@testing-library/react'

configure({ asyncUtilTimeout: 8000 })
vi.setConfig({ testTimeout: 20000 })
import '../../i18n.js'

// The card is mounted next to a finished turn's tool calls in a coder chat, from the call's result, and draws the
// stored proposal of the conversation it belongs to.
const { api } = vi.hoisted(() => ({
  api: {
    getChatTurns: vi.fn(), getChatEvents: vi.fn(), startChatTurn: vi.fn(), stopChatTurn: vi.fn(), coderStatus: vi.fn(),
    coderWorkspaceList: vi.fn(), getAgentRuntimeModels: vi.fn(), getProfiles: vi.fn(), listChatConversations: vi.fn(),
    browserPermissions: vi.fn(), listOrgDesigns: vi.fn(), getWorkflow: vi.fn(), getOrgProposal: vi.fn(),
  },
}))
vi.mock('../../services/api.js', () => ({
  api, notify: vi.fn(), onChatEvent: () => () => {}, onOrgEvent: () => () => {}, onOrgRunStatus: () => () => {}, newOrgEventsStreamId: () => 's',
}))
vi.mock('../../lib/chatAccessApi.js', () => ({ chatAccessApi: { create: vi.fn(), setAccess: vi.fn() } }))
vi.mock('../ConfirmDialog.jsx', () => ({ confirm: vi.fn() }))

import CoderBubbles from './CoderBubbles.jsx'
import { useCoderBubbles } from './useCoderBubbles.js'

const ID = '0123456789abcdef0123456789abcdef'
function Harness() {
  const store = useCoderBubbles()
  useEffect(() => { store.openConversation({ id: 's1', access: 'scoped', runtimeId: 'claude', model: 'm' }) }, []) // eslint-disable-line react-hooks/exhaustive-deps
  return <CoderBubbles store={store} onNavigate={() => {}} onOpenArtifact={() => {}} />
}
const ev = (seq, type, payload) => ({ conversationId: 's1', turnId: 't1', seq, type, payload, at: '2026-10-22T10:00:00Z' })
const stored = (over = {}) => ({ v: 1, proposal: {
  id: ID, org: 'acme', conversation_id: 's1', verb: 'grant.add', status: 'pending', current: true, hash: 'd'.repeat(64),
  preview_text: 'STORED preview', created_by: 'chat', expires_at: new Date(Date.now() + 600000).toISOString(), ...over } })

beforeEach(() => {
  vi.clearAllMocks()
  Element.prototype.scrollTo = function scrollTo() {}
  localStorage.clear()
  api.getChatTurns.mockResolvedValue({ items: [{ id: 't1', prompt: 'let lead publish', status: 'completed', ownedByThisInstance: true }] })
  api.getChatEvents.mockResolvedValue({ hasMore: false, lastCommittedSeq: 4, items: [
    ev(1, 'turn.started', {}),
    ev(2, 'tool.started', { callId: 'c1', name: 'set_org_grant', arguments: '{}' }),
    ev(3, 'tool.completed', { callId: 'c1', ok: true, result: JSON.stringify({ applied: false, proposal_id: ID, preview_text: 'TOOL TEXT', status: 'pending' }) }),
    ev(4, 'turn.finished', { status: 'completed' }),
  ] })
  api.coderStatus.mockResolvedValue({ enabled: false })
  api.coderWorkspaceList.mockResolvedValue([])
  api.getProfiles.mockResolvedValue([])
  api.listChatConversations.mockResolvedValue({ items: [] })
  api.browserPermissions.mockResolvedValue({ kinds: [], catalog: [] })
  api.listOrgDesigns.mockResolvedValue([])
})
afterEach(cleanup)

describe('an org proposal in a finished turn', () => {
  it('shows the stored preview, not the tool text', async () => {
    api.getOrgProposal.mockResolvedValue(stored())
    render(<Harness />)
    expect(await screen.findByTestId('org-proposal-preview')).toHaveTextContent('STORED preview')
    expect(screen.queryByText('TOOL TEXT')).toBeNull()
    expect(api.getOrgProposal).toHaveBeenCalledWith(ID)
  })

  it('refuses a proposal whose verb is not the tool\'s', async () => {
    api.getOrgProposal.mockResolvedValue(stored({ verb: 'autonomy.set' }))
    render(<Harness />)
    expect(await screen.findByTestId('org-proposal-unavailable')).toBeInTheDocument()
    await waitFor(() => expect(screen.queryByTestId('org-proposal-confirm')).toBeNull())
  })
})
