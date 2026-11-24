// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import '../i18n.js'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import { WorkflowsModal } from './NodeRunnerModals.jsx'

// The engine writes upper-case statuses; the per-workflow executions list
// must read them like every other run list does.
const execs = [
  { id: 'e-ok', workflow_id: 'wf1', status: 'SUCCESS', started_at: '' },
  { id: 'e-run', workflow_id: 'wf1', status: 'RUNNING', started_at: '' },
  { id: 'e-bad', workflow_id: 'wf1', status: 'FAILED', error: 'node http.request: 500', started_at: '' },
]
vi.mock('../wailsjs/go/main/App', async (importOriginal) => ({
  ...await importOriginal(),
  ListWorkflows: vi.fn(() => Promise.resolve([{ id: 'wf1', name: 'Flow', node_count: 1 }])),
  ListWorkflowTemplates: vi.fn(() => Promise.resolve([])),
  GetWorkflowExecutions: vi.fn(() => Promise.resolve(execs)),
}))
vi.mock('../services/api.js', () => ({ api: {}, notify: vi.fn() }))

afterEach(cleanup)

async function openList(props = {}) {
  render(<WorkflowsModal onLoad={vi.fn()} onDelete={vi.fn()} onClose={vi.fn()} {...props} />)
  fireEvent.click(await screen.findByTitle(/executions/i))
  await screen.findByText('e-ok')
}

describe('WorkflowsModal executions list', () => {
  it('shows the error text of a failed run', async () => {
    await openList()
    expect(screen.getByText(/node http.request: 500/)).toBeInTheDocument()
  })
  it('colours by the real status, not a lower-case compare', async () => {
    await openList()
    const dot = (id) => screen.getByText(id).closest('[data-exec-row]').querySelector('span[style*="border-radius"]')
    expect(dot('e-ok').style.background).toBe('var(--green-neon)')
    expect(dot('e-run').style.background).toBe('var(--cyan)')
    expect(dot('e-bad').style.background).toBe('rgb(239, 68, 68)')
  })
  it('opens the execution when its row is clicked', async () => {
    const onOpenExecution = vi.fn()
    await openList({ onOpenExecution })
    fireEvent.click(screen.getByText('e-bad'))
    await waitFor(() => expect(onOpenExecution).toHaveBeenCalledWith('wf1', 'e-bad'))
  })
})
