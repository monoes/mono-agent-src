// @vitest-environment jsdom
// W4: a failed mark-read reverts the dot; a full 200-row fetch says the view is capped.
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import '../i18n.js'

const getAll = vi.fn()
const markRead = vi.fn()
vi.mock('../services/api.js', () => ({
  api: { getAllPersonMessages: (...a) => getAll(...a), markPersonMessagesRead: (...a) => markRead(...a) },
}))
vi.mock('../components/MessageDetailModal.jsx', () => ({ default: () => null }))
import Communications from './Communications.jsx'

afterEach(() => { cleanup(); getAll.mockReset(); markRead.mockReset() })

describe('Communications W4', () => {
  it('puts the unread dot back when marking read fails', async () => {
    getAll.mockResolvedValue([{ id: 'm1', person_id: 'p1', direction: 'inbound', source: 'x', subject: 'Hello', person_platform_username: 'sam' }])
    markRead.mockRejectedValue(new Error('cli down'))
    render(<Communications onProfile={vi.fn()} />)
    await screen.findByText('Hello')
    fireEvent.click(screen.getByText('Hello'))
    await waitFor(() => expect(screen.getAllByTitle('Unread')).toHaveLength(1))
  })

  it('says the list is capped when the fetch came back full', async () => {
    getAll.mockResolvedValue(Array.from({ length: 200 }, (_, i) => ({ id: `m${i}`, person_id: 'p', direction: 'inbound', source: 'x', subject: `s${i}`, read_at: 'x', person_platform_username: 'a' })))
    render(<Communications onProfile={vi.fn()} />)
    await screen.findByText('s0')
    expect(screen.getByTestId('comms-capped')).toBeInTheDocument()
  })
})
