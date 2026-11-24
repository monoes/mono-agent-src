// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import { WorkflowsModal } from './NodeRunnerModals.jsx'

const m = vi.hoisted(() => ({ list: vi.fn(), execs: vi.fn(), notify: vi.fn() }))
vi.mock('../wailsjs/go/main/App', async (importOriginal) => ({
  ...await importOriginal(),
  ListWorkflows: (...a) => m.list(...a),
  ListWorkflowTemplates: async () => [],
  GetWorkflowExecutions: (...a) => m.execs(...a),
}))
vi.mock('../services/api.js', () => ({ notify: (...a) => m.notify(...a) }))
import '../i18n.js'
vi.mock('../components/library/LibraryModal.jsx', () => ({ default: () => null }))
vi.mock('../components/library/LogInToMonoesButton.jsx', () => ({ default: () => null }))

afterEach(() => { cleanup(); vi.clearAllMocks() })
const props = { currentId: null, onLoad: vi.fn(), onClose: vi.fn() }

describe('WorkflowsModal failures', () => {
  it('a failed list shows the error with a retry, not "No saved workflows yet"', async () => {
    m.list.mockRejectedValueOnce(new Error('db locked')).mockResolvedValueOnce([{ id: 'w1', name: 'Mine', nodes: [] }])
    render(<WorkflowsModal {...props} onDelete={vi.fn()} />)
    expect(await screen.findByText(/db locked/)).toBeInTheDocument()
    expect(screen.queryByText('No saved workflows yet')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: /retry/i }))
    expect(await screen.findByText('Mine')).toBeInTheDocument()
  })
  it('a failed executions fetch shows the error with a retry, not "No executions found"', async () => {
    m.list.mockResolvedValue([{ id: 'w1', name: 'Mine', nodes: [] }])
    m.execs.mockRejectedValueOnce(new Error('timeout')).mockResolvedValueOnce([{ id: 'e1', status: 'success' }])
    render(<WorkflowsModal {...props} onDelete={vi.fn()} />)
    fireEvent.click(await screen.findByTitle('View executions'))
    expect(await screen.findByText(/timeout/)).toBeInTheDocument()
    expect(screen.queryByText('No executions found')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: /retry/i }))
    expect(await screen.findByText('e1')).toBeInTheDocument()
  })
  it('a delete that throws is reported and keeps the row', async () => {
    m.list.mockResolvedValue([{ id: 'w1', name: 'Mine', nodes: [] }])
    render(<WorkflowsModal {...props} onDelete={async () => { throw new Error('in use') }} />)
    fireEvent.click(await screen.findByTitle('Delete'))
    await waitFor(() => expect(m.notify).toHaveBeenCalledWith('delete workflow', expect.stringContaining('in use')))
    expect(screen.getByText('Mine')).toBeInTheDocument()
  })
})
