// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
import Documents from './Documents.jsx'
import * as WailsApp from '../wailsjs/go/main/App'
import { onApiError } from '../services/api.js'

vi.mock('../components/ConfirmDialog.jsx', () => ({
  confirm: vi.fn(() => Promise.resolve(true)),
}))

vi.mock('../wailsjs/go/main/App', () => ({
  ListProfileDocuments: vi.fn(),
  IndexProfileDocument: vi.fn(),
  DeleteProfileDocument: vi.fn(),
  RemoveProfileDocument: vi.fn(),
  ListExcludedDocuments: vi.fn(),
  RestoreExcludedDocument: vi.fn(),
  UploadProfileDocument: vi.fn(),
  OpenAnyFilePicker: vi.fn(),
  OpenFolderFilesPicker: vi.fn(),
  UploadProfileDocuments: vi.fn(),
  SearchProfileKnowledge: vi.fn(),
  AddDocumentTag: vi.fn(),
  RemoveDocumentTag: vi.fn(),
  IsMonomindInitialized: vi.fn(() => Promise.resolve(true)),
}))

// indexed & fresh — "Index all"/"Index selected" must skip this one
const freshDoc = { id: 'doc-1', filename: 'resume.pdf', source: 'upload', size_bytes: 100, created_at: '2026-01-01', indexed: true, stale: false }
// never indexed — needs indexing
const notIndexedDoc = { id: 'doc-2', filename: 'notes.md', source: 'upload', size_bytes: 200, created_at: '2026-01-02', indexed: false, stale: false }
// indexed but stale — needs re-indexing; auto-discovered ("Profile folder")
const staleDiscoveredDoc = { id: 'doc-3', filename: 'AGENTS.md', source: 'discovered', size_bytes: 300, created_at: '2026-01-03', indexed: true, stale: true }

beforeEach(() => {
  vi.clearAllMocks()
  WailsApp.ListProfileDocuments.mockResolvedValue([freshDoc, notIndexedDoc, staleDiscoveredDoc])
  WailsApp.IndexProfileDocument.mockResolvedValue({ indexed: true })
  WailsApp.DeleteProfileDocument.mockResolvedValue(undefined)
  WailsApp.RemoveProfileDocument.mockResolvedValue(undefined)
  WailsApp.ListExcludedDocuments.mockResolvedValue([])
  WailsApp.RestoreExcludedDocument.mockResolvedValue(undefined)
})

afterEach(() => {
  cleanup()
})

async function renderLoaded() {
  render(<Documents />)
  await waitFor(() => expect(screen.getByText('resume.pdf')).toBeInTheDocument())
}

describe('Documents multi-select', () => {
  it('renders a checkbox per row and a header select-all checkbox', async () => {
    await renderLoaded()
    const checkboxes = screen.getAllByRole('checkbox')
    // 1 header + 3 rows
    expect(checkboxes).toHaveLength(4)
  })

  it('header checkbox selects and deselects all visible rows', async () => {
    await renderLoaded()
    const [header, row1, row2, row3] = screen.getAllByRole('checkbox')
    fireEvent.click(header)
    expect(row1.checked).toBe(true)
    expect(row2.checked).toBe(true)
    expect(row3.checked).toBe(true)
    fireEvent.click(header)
    expect(row1.checked).toBe(false)
  })

  it('hides "Index selected"/"Delete selected" with no selection, shows them once something is checked', async () => {
    await renderLoaded()
    expect(screen.queryByRole('button', { name: /Index selected/ })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Delete selected/ })).not.toBeInTheDocument()

    const [, row1] = screen.getAllByRole('checkbox')
    fireEvent.click(row1)

    expect(screen.getByRole('button', { name: /Index selected/ })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Delete selected/ })).toBeInTheDocument()
  })
})

describe('Documents "Index all"', () => {
  it('indexes only the documents that need it, skipping already-indexed-and-fresh ones', async () => {
    await renderLoaded()

    fireEvent.click(screen.getByRole('button', { name: /Index all/ }))

    await waitFor(() => expect(WailsApp.IndexProfileDocument).toHaveBeenCalledTimes(2))
    expect(WailsApp.IndexProfileDocument).toHaveBeenCalledWith('doc-2')
    expect(WailsApp.IndexProfileDocument).toHaveBeenCalledWith('doc-3')
    expect(WailsApp.IndexProfileDocument).not.toHaveBeenCalledWith('doc-1')
  })
})

describe('Documents "Delete selected"', () => {
  it('confirms once, deleting uploads and removing auto-discovered documents from MonoAgent', async () => {
    await renderLoaded()

    const [, row1, , row3] = screen.getAllByRole('checkbox')
    fireEvent.click(row1) // freshDoc, source: upload
    fireEvent.click(row3) // staleDiscoveredDoc, source: discovered

    fireEvent.click(screen.getByRole('button', { name: /Delete selected/ }))

    const { confirm } = await import('../components/ConfirmDialog.jsx')
    await waitFor(() => expect(confirm).toHaveBeenCalledTimes(1))
    // an auto-discovered document in the selection offers the on-disk choice
    expect(confirm.mock.calls[0][1].checkbox.label).toMatch(/delete the file from the profile folder on disk/i)
    expect(confirm.mock.calls[0][0]).toMatch(/1 of them is in your profile folder/)

    await waitFor(() => expect(WailsApp.RemoveProfileDocument).toHaveBeenCalledTimes(1))
    expect(WailsApp.DeleteProfileDocument).toHaveBeenCalledWith('doc-1')
    expect(WailsApp.RemoveProfileDocument).toHaveBeenCalledWith('doc-3', false) // file kept by default
  })

  it('passes deleteFile when the box is ticked, and offers no box for uploads only', async () => {
    const { confirm } = await import('../components/ConfirmDialog.jsx')
    confirm.mockResolvedValueOnce({ checked: true })
    await renderLoaded()
    fireEvent.click(screen.getByLabelText('Select AGENTS.md'))
    fireEvent.click(screen.getByRole('button', { name: /Delete selected/ }))
    await waitFor(() => expect(WailsApp.RemoveProfileDocument).toHaveBeenCalledWith('doc-3', true))

    confirm.mockClear()
    fireEvent.click(screen.getByLabelText('Select resume.pdf'))
    fireEvent.click(screen.getByRole('button', { name: /Delete selected/ }))
    await waitFor(() => expect(confirm).toHaveBeenCalledTimes(1))
    expect(confirm.mock.calls[0][1].checkbox).toBeUndefined()
  })

  it('lists excluded documents behind "Show excluded" and restores one', async () => {
    WailsApp.ListExcludedDocuments.mockResolvedValue([{ path: '/p/career-profile.md', relative_path: 'career-profile.md', filename: 'career-profile.md', excluded_at: 't' }])
    await renderLoaded()
    expect(screen.queryByText('career-profile.md')).not.toBeInTheDocument()
    fireEvent.click(await screen.findByRole('button', { name: /Show excluded \(1\)/ }))
    expect(screen.getByText('career-profile.md')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Restore career-profile.md' }))
    await waitFor(() => expect(WailsApp.RestoreExcludedDocument).toHaveBeenCalledWith('/p/career-profile.md'))
  })
})

describe('Documents tags', () => {
  const tagged = { ...freshDoc, tags: [{ id: 't1', name: 'contract', color: '#ff0000' }] }

  it('adds a tag to a document and shows it', async () => {
    WailsApp.AddDocumentTag.mockResolvedValue({ id: 't2', name: 'urgent', color: '#00b4d8' })
    await renderLoaded()
    fireEvent.click(screen.getByRole('button', { name: 'Add tag to notes.md' }))
    const input = screen.getByLabelText('New tag for notes.md')
    fireEvent.change(input, { target: { value: 'urgent' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    await waitFor(() => expect(WailsApp.AddDocumentTag).toHaveBeenCalledWith('doc-2', 'urgent'))
    await waitFor(() => expect(screen.getAllByText('urgent').length).toBeGreaterThan(0))
  })

  it('removes a tag from a document', async () => {
    WailsApp.ListProfileDocuments.mockResolvedValue([tagged, notIndexedDoc])
    WailsApp.RemoveDocumentTag.mockResolvedValue(undefined)
    await renderLoaded()
    fireEvent.click(screen.getByRole('button', { name: 'Remove tag contract' }))
    await waitFor(() => expect(WailsApp.RemoveDocumentTag).toHaveBeenCalledWith('doc-1', 't1'))
    await waitFor(() => expect(screen.queryByText('contract')).not.toBeInTheDocument())
  })

  it('filters the list by clicking a tag', async () => {
    WailsApp.ListProfileDocuments.mockResolvedValue([tagged, notIndexedDoc])
    await renderLoaded()
    expect(screen.getByText('notes.md')).toBeInTheDocument()
    fireEvent.click(screen.getAllByText('contract')[0])
    expect(screen.queryByText('notes.md')).not.toBeInTheDocument()
    expect(screen.getByText('resume.pdf')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Clear' }))
    expect(screen.getByText('notes.md')).toBeInTheDocument()
  })
})

describe('Documents folder upload', () => {
  it('shows why the folder dialog could not be shown (another dialog is open) instead of nothing', async () => {
    WailsApp.OpenFolderFilesPicker.mockRejectedValue('another dialog is still open: answer or close it first')
    const seen = []
    const off = onApiError((d) => seen.push(d))
    await renderLoaded()
    fireEvent.click(screen.getByRole('button', { name: /Upload folder/ }))
    await waitFor(() => expect(seen.length).toBe(1))
    off()
    expect(seen[0].op).toBe('upload')
    expect(seen[0].message).toMatch(/another dialog is still open/)
    expect(WailsApp.UploadProfileDocuments).not.toHaveBeenCalled()
  })
})
