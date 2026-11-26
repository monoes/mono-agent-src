// @vitest-environment jsdom
// HIL page, AskCard and the detail modal (#479): links to the recipient and
// to Communications, the Communications refresh after a decision, and the
// status timeline.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
import '../i18n.js'
import * as WailsApp from '../wailsjs/go/main/App'
import { onCommsChanged } from '../lib/commsStatus.js'
import { normalizeAsk } from '../lib/askModel.js'
import sendEmail from '../components/bubbles/__fixtures__/hil-send-email.json'
import workflow from '../components/bubbles/__fixtures__/hil-workflow.json'

vi.mock('../wailsjs/go/main/App', () => ({
  GetHILItems: vi.fn(), ApproveHIL: vi.fn(), RejectHIL: vi.fn(),
  GetDraftPersonMessages: vi.fn(), SendDraftPersonMessage: vi.fn(), RejectDraftPersonMessage: vi.fn(),
}))
vi.mock('../services/api.js', () => ({
  api: {
    getPendingPeopleApprovals: vi.fn(() => Promise.resolve([])), approvePendingPerson: vi.fn(), rejectPendingPerson: vi.fn(),
    listOrgPending: vi.fn(() => Promise.resolve({ items: [] })),
  },
  notify: vi.fn(),
}))
vi.mock('../lib/usePageVisible.js', () => ({ usePageVisibleRef: () => ({ current: true }), useVisibleCatchUp: () => {} }))
const { hilApi } = vi.hoisted(() => ({ hilApi: { attachment: vi.fn(), openPath: vi.fn(), openUrl: vi.fn() } }))
vi.mock('../services/hilApi.js', () => hilApi)

import HumanInLoop from './HumanInLoop.jsx'
import { AskCard } from '../components/bubbles/AskCard.jsx'
import MessageDetailModal from '../components/MessageDetailModal.jsx'

beforeEach(() => { vi.clearAllMocks(); hilApi.attachment.mockResolvedValue({ dataUrl: 'data:image/png;base64,AAAA' }) })
afterEach(cleanup)

describe('HIL page links and refresh', () => {
  const item = { id: 'h1', node_name: 'Review', workflow_name: 'wf', execution_id: 'abcdefgh1234', created_at: '2026-10-01 10:00:00', editable_data: {}, readonly_data: {}, person_id: 'p-7', hil_ref: 'h1' }

  it('links the recipient to People and the item to Communications', async () => {
    WailsApp.GetHILItems.mockResolvedValue([item])
    WailsApp.GetDraftPersonMessages.mockResolvedValue([])
    const onProfile = vi.fn(); const onNavigate = vi.fn()
    render(<HumanInLoop onProfile={onProfile} onNavigate={onNavigate} />)
    fireEvent.click(await screen.findByText('Open person'))
    expect(onProfile).toHaveBeenCalledWith('p-7')
    fireEvent.click(screen.getByText('See in Communications'))
    expect(onNavigate).toHaveBeenCalledWith('communications', { hil_ref: 'h1', person: '' })
  })

  it('shows no links for an item without a person or ref', async () => {
    WailsApp.GetHILItems.mockResolvedValue([{ ...item, person_id: undefined, hil_ref: undefined }])
    WailsApp.GetDraftPersonMessages.mockResolvedValue([])
    render(<HumanInLoop onProfile={vi.fn()} onNavigate={vi.fn()} />)
    await screen.findByText('Review')
    expect(screen.queryByTestId('comms-links')).toBeNull()
  })

  it('tells Communications to refresh after a HIL approve', async () => {
    WailsApp.GetHILItems.mockResolvedValue([item])
    WailsApp.GetDraftPersonMessages.mockResolvedValue([])
    WailsApp.ApproveHIL.mockResolvedValue()
    const heard = vi.fn(); const off = onCommsChanged(heard)
    render(<HumanInLoop onProfile={vi.fn()} onNavigate={vi.fn()} />)
    fireEvent.click(await screen.findByText(/Approve/))
    await waitFor(() => expect(heard).toHaveBeenCalled())
    off()
  })
})

describe('AskCard links', () => {
  it('a send ask links to Communications by its HIL ref', () => {
    const onNavigate = vi.fn()
    render(<AskCard ask={normalizeAsk(sendEmail)} onNavigate={onNavigate} />)
    fireEvent.click(screen.getByText('See in Communications'))
    expect(onNavigate).toHaveBeenCalledWith('communications', { hil_ref: 'hil-send-1' })
    fireEvent.click(screen.getByText('open person'))
    expect(onNavigate).toHaveBeenCalledWith('people', { person: 'p-42' })
  })

  it('a workflow HIL ask with a person and ref links to both; without, to neither', () => {
    const onNavigate = vi.fn()
    const { unmount } = render(<AskCard ask={normalizeAsk({ ...workflow, person_id: 'p-3', hil_ref: 'r-3' })} onNavigate={onNavigate} />)
    fireEvent.click(screen.getByTestId('ask-recipient-link'))
    expect(onNavigate).toHaveBeenCalledWith('people', { person: 'p-3' })
    fireEvent.click(screen.getByTestId('ask-see-comms'))
    expect(onNavigate).toHaveBeenCalledWith('communications', { hil_ref: 'r-3' })
    unmount()
    render(<AskCard ask={normalizeAsk(workflow)} onNavigate={onNavigate} />)
    expect(screen.queryByTestId('ask-see-comms')).toBeNull()
  })
})

describe('MessageDetailModal status timeline', () => {
  it('shows the metadata history, else the current status', () => {
    const base = { id: 'm', direction: 'outbound', subject: 's', body: 'hello', source: 'x', status: 'sent', sent_at: '2026-10-01T11:00:00Z' }
    const { unmount } = render(<MessageDetailModal message={{ ...base, metadata: { status_history: [{ status: 'pending_approval', at: '2026-10-01T10:00:00Z' }, { status: 'approved', at: '2026-10-01T10:30:00Z' }, { status: 'sent', at: '2026-10-01T11:00:00Z' }] } }} onClose={vi.fn()} />)
    expect(screen.getAllByTestId('comms-status').map(e => e.dataset.status)).toEqual(['pending_approval', 'approved', 'sent'])
    unmount()
    render(<MessageDetailModal message={base} onClose={vi.fn()} />)
    expect(screen.getAllByTestId('comms-status').map(e => e.dataset.status)).toEqual(['sent'])
  })
})
