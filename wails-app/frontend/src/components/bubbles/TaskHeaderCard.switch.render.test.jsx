// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, waitFor, cleanup } from '@testing-library/react'
import '../../i18n.js'

const { aitaskApi, aitaskApplyApi } = vi.hoisted(() => ({
  aitaskApi: { get: vi.fn(), cancel: vi.fn() },
  aitaskApplyApi: { preview: vi.fn(), apply: vi.fn(), saveDraft: vi.fn() },
}))
vi.mock('../SendToCoderApi.js', async (orig) => ({ ...(await orig()), aitaskApi, aitaskApplyApi }))
vi.mock('../settings/aitaskBudgetApi.js', () => ({ aitaskBudgetApi: { forTask: vi.fn(() => Promise.resolve(null)) } }))
vi.mock('../ConfirmDialog.jsx', () => ({ confirm: vi.fn() }))

import { TaskHeaderCard } from './TaskHeaderCard.jsx'

afterEach(() => { cleanup(); vi.clearAllMocks() })

const fix = { diagnosis: 'Diagnosis of task A', workflowId: 'w1', executionId: 'e1', fix: { kind: 'workflow-edit', description: 'd', edits: [{ node: 'n', config: {} }] } }

describe('TaskHeaderCard on a task switch', () => {
  it('shows the new task\'s running state, none of the old result, and no enabled Apply', async () => {
    aitaskApi.get.mockImplementation(id => Promise.resolve(id === 'tA' ? { status: 'complete', kind: 'failed-run-fix', result: fix } : { status: 'running', kind: 'failed-run-fix' }))
    aitaskApplyApi.preview.mockResolvedValue({ ok: true, definitionHash: 'sha256:aaaa1111', workflowId: 'w1', edits: [{ node: 'n', nodeName: 'N', path: 'p', before: 'a', after: 'b' }], warnings: [] })
    const A = { taskId: 'tA', kind: 'failed-run-fix', goal: 'goal A', status: 'complete' }
    const B = { taskId: 'tB', kind: 'failed-run-fix', goal: 'goal B', status: 'running' }
    const { rerender } = render(<TaskHeaderCard task={A} />)
    await screen.findByText('Diagnosis of task A')
    rerender(<TaskHeaderCard task={B} />)
    expect(screen.queryByText('Diagnosis of task A')).toBeNull()
    expect(screen.getByTestId('task-header')).toHaveTextContent('running')
    expect(screen.getByTestId('task-header')).not.toHaveTextContent('complete')
    expect(screen.queryByTestId('fix-diff')).toBeNull()
    const apply = screen.queryByRole('button', { name: 'Apply fix' })
    expect(apply === null || apply.disabled).toBe(true)
    await waitFor(() => expect(aitaskApi.get).toHaveBeenCalled())
    expect(screen.queryByTestId('task-result')).toBeNull()
  })
})
