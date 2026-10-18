// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import '../i18n.js'
import Publication from './Publication.jsx'
import { api } from '../services/api.js'

vi.mock('../services/api.js', () => ({ api: { listPublications: vi.fn(), getPublicationStats: vi.fn(), getPublication: vi.fn(), openURL: vi.fn() } }))
const entry = { id: 'p1', platform: 'x', kind: 'post', title: 'Published title', body: 'Full published content', url: 'https://example.com/post', workflow_id: 'w1', execution_id: 'e1', agent_id: 'a1', published_at: '2026-10-06T12:00:00Z', media: ['asset.png'] }
beforeEach(() => {
  vi.clearAllMocks()
  api.listPublications.mockResolvedValue([entry])
  api.getPublicationStats.mockResolvedValue({ total: 1, by_platform: { x: 1 }, by_kind: { post: 1 } })
  api.getPublication.mockResolvedValue(entry)
})
afterEach(cleanup)

describe('Publication', () => {
  it('applies server filters and opens full detail and execution', async () => {
    const navigate = vi.fn()
    render(<Publication onNavigate={navigate} />)
    await screen.findByText('Published title')
    fireEvent.change(screen.getByLabelText('Search'), { target: { value: 'full' } })
    fireEvent.change(screen.getByLabelText('Platform'), { target: { value: 'x' } })
    fireEvent.change(screen.getByLabelText('Kind'), { target: { value: 'post' } })
    fireEvent.click(screen.getByText('Apply filters'))
    await waitFor(() => expect(api.listPublications).toHaveBeenLastCalledWith(expect.objectContaining({ search: 'full', platform: 'x', kind: 'post', offset: 0 })))
    fireEvent.click(await screen.findByText('Published title'))
    const dialog = await screen.findByRole('dialog')
    await within(dialog).findByText('Full published content')
    expect(within(dialog).getByText('asset.png')).toBeInTheDocument()
    const close = within(dialog).getByRole('button', { name: 'Close' })
    expect(close).toHaveFocus()
    fireEvent.keyDown(close, { key: 'Tab', shiftKey: true })
    expect(within(dialog).getByText('View execution')).toHaveFocus()
    fireEvent.click(within(dialog).getByText('Open publication'))
    expect(api.openURL).toHaveBeenCalledWith(entry.url)
    fireEvent.click(within(dialog).getByText('View execution'))
    expect(navigate).toHaveBeenCalledWith('noderunner', { executionId: 'e1', workflowId: 'w1' })
  })
  it('pages from an extra row and refreshes on activation and profile changes', async () => {
    api.listPublications.mockResolvedValue(Array.from({ length: 51 }, (_, i) => ({ ...entry, id: `p${i}`, title: `Title ${i}` })))
    const { rerender } = render(<Publication profileId="one" />)
    await screen.findByText('Title 0')
    expect(screen.queryByText('Title 50')).not.toBeInTheDocument()
    fireEvent.click(screen.getByText('Next'))
    await waitFor(() => expect(api.listPublications).toHaveBeenLastCalledWith(expect.objectContaining({ offset: 50 })))
    rerender(<Publication profileId="two" />)
    await waitFor(() => expect(api.listPublications).toHaveBeenLastCalledWith(expect.objectContaining({ offset: 0 })))
    rerender(<Publication profileId="two" isActive={false} />)
    const count = api.listPublications.mock.calls.length
    rerender(<Publication profileId="two" isActive />)
    await waitFor(() => expect(api.listPublications.mock.calls.length).toBeGreaterThan(count))
  })
  it('shows empty, loading and failed history distinctly', async () => {
    api.listPublications.mockResolvedValue([])
    const { unmount } = render(<Publication />)
    expect(screen.getByRole('status')).toHaveTextContent('Loading')
    await screen.findByText('No publications found')
    unmount()
    api.listPublications.mockRejectedValue(new Error('database unavailable'))
    render(<Publication />)
    expect(await screen.findByRole('alert')).toHaveTextContent('database unavailable')
    expect(screen.queryByText('No publications found')).not.toBeInTheDocument()
  })
  it('shows detail errors and restores focus when closed with Escape', async () => {
    api.getPublication.mockRejectedValue(new Error('publication removed'))
    render(<Publication />)
    const title = await screen.findByText('Published title')
    const row = title.closest('button')
    row.focus()
    fireEvent.click(row)
    const dialog = await screen.findByRole('dialog')
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('publication removed')
    fireEvent.keyDown(dialog, { key: 'Escape' })
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(row).toHaveFocus()
  })
  it('does not link standalone node runs to workflow execution history', async () => {
    api.getPublication.mockResolvedValue({ ...entry, workflow_id: 'cli', execution_id: 'standalone-run' })
    render(<Publication onNavigate={vi.fn()} />)
    fireEvent.click(await screen.findByText('Published title'))
    const dialog = await screen.findByRole('dialog')
    await within(dialog).findByText('standalone-run')
    expect(within(dialog).queryByText('View execution')).not.toBeInTheDocument()
  })
  it('does not open executable remote URLs', async () => {
    api.getPublication.mockResolvedValue({ ...entry, url: 'javascript:alert(1)' })
    render(<Publication />)
    fireEvent.click(await screen.findByText('Published title'))
    const dialog = await screen.findByRole('dialog')
    await within(dialog).findByText('javascript:alert(1)')
    expect(within(dialog).queryByText('Open publication')).not.toBeInTheDocument()
  })
})
