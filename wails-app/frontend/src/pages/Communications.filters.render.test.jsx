// @vitest-environment jsdom
// R20-13: the source, status and unread filters go to the CLI (before its
// limit); switching one reloads and keeps the other choices on screen.
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import '../i18n.js'

const getAll = vi.fn()
vi.mock('../services/api.js', () => ({
  api: { getAllPersonMessages: (...a) => getAll(...a), markPersonMessagesRead: vi.fn(() => Promise.resolve(null)) },
}))
vi.mock('../components/MessageDetailModal.jsx', () => ({ default: () => null }))
import Communications from './Communications.jsx'

afterEach(() => { cleanup(); getAll.mockReset() })

const row = (id, source, extra = {}) => ({ id, person_id: 'p', direction: 'inbound', source, subject: `s-${id}`, person_platform_username: 'a', ...extra })

describe('Communications server-side filters', () => {
  it('asks the CLI for the chosen source and unread, and keeps the other sources to pick', async () => {
    getAll.mockImplementation((_limit, f) => Promise.resolve(f?.source === 'outlook' ? [row('o1', 'outlook')] : [row('o1', 'outlook'), row('l1', 'linkedin')]))
    render(<Communications onProfile={vi.fn()} />)
    await screen.findByText('s-l1')
    expect(getAll).toHaveBeenLastCalledWith(200, { source: '', status: '', unread: false })
    fireEvent.click(screen.getByRole('button', { name: /^outlook/ }))
    await waitFor(() => expect(getAll).toHaveBeenLastCalledWith(200, { source: 'outlook', status: '', unread: false }))
    await waitFor(() => expect(screen.queryByText('s-l1')).toBeNull())
    expect(screen.getByRole('button', { name: /^linkedin/ })).toBeInTheDocument()
    fireEvent.click(screen.getByText(/^Unread/))
    await waitFor(() => expect(getAll).toHaveBeenLastCalledWith(200, { source: 'outlook', status: '', unread: true }))
  })

  it('asks for one exact status, but filters grouped statuses in the page', async () => {
    getAll.mockResolvedValue([row('d1', 'x', { direction: 'outbound', status: 'draft' })])
    render(<Communications onProfile={vi.fn()} />)
    await screen.findByText('s-d1')
    fireEvent.click(screen.getByTestId('status-filter-pending_approval'))
    await waitFor(() => expect(getAll).toHaveBeenLastCalledWith(200, { source: '', status: 'pending_approval', unread: false }))
    // An empty result keeps the status chips, so the filter can be left.
    await waitFor(() => expect(screen.getByTestId('status-filter-all')).toBeInTheDocument())
    fireEvent.click(screen.getByTestId('status-filter-failed'))
    await waitFor(() => expect(getAll).toHaveBeenLastCalledWith(200, { source: '', status: '', unread: false }))
  })
})
