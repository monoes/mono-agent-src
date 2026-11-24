// @vitest-environment jsdom
// W5 R21-27: the executions view shows a loading row while it loads.
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import '../i18n.js'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'
import { WorkflowsModal } from './NodeRunnerModals.jsx'
import * as WailsApp from '../wailsjs/go/main/App'

vi.mock('../wailsjs/go/main/App', async (importOriginal) => ({
  ...await importOriginal(),
  ListWorkflows: vi.fn(() => Promise.resolve([{ id: 'wfA', name: 'A', node_count: 1 }])),
  ListWorkflowTemplates: vi.fn(() => Promise.resolve([])),
  GetWorkflowExecutions: vi.fn(),
}))
vi.mock('../services/api.js', () => ({ api: {}, notify: vi.fn() }))

afterEach(cleanup)

describe('WorkflowsModal executions loading', () => {
  it('shows a loading row while the executions load', async () => {
    WailsApp.GetWorkflowExecutions.mockImplementation(() => new Promise(() => {}))
    render(<WorkflowsModal onLoad={vi.fn()} onDelete={vi.fn()} onClose={vi.fn()} />)
    fireEvent.click(await screen.findByTitle(/executions/i))
    expect(await screen.findByText(/loading/i)).toBeInTheDocument()
  })
})
