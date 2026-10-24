// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
import '../../i18n.js'

const { aitaskApi, aitaskApplyApi, aitaskBudgetApi, confirm } = vi.hoisted(() => ({
  aitaskApi: { get: vi.fn(), cancel: vi.fn() },
  aitaskApplyApi: { preview: vi.fn(), apply: vi.fn(), saveDraft: vi.fn() },
  aitaskBudgetApi: { forTask: vi.fn() },
  confirm: vi.fn(),
}))
vi.mock('../SendToCoderApi.js', async (orig) => ({ ...(await orig()), aitaskApi, aitaskApplyApi }))
vi.mock('../settings/aitaskBudgetApi.js', async (orig) => ({ ...(await orig()), aitaskBudgetApi }))
vi.mock('../ConfirmDialog.jsx', () => ({ confirm }))

import { TaskHeaderCard } from './TaskHeaderCard.jsx'

beforeEach(() => { aitaskBudgetApi.forTask.mockResolvedValue(null) })
afterEach(() => { cleanup(); vi.clearAllMocks() })

function done(kind, result, onNavigate) {
  aitaskApi.get.mockResolvedValue({ status: 'complete', kind, result })
  render(<TaskHeaderCard task={{ taskId: 't1', kind, goal: 'g', status: 'complete' }} onNavigate={onNavigate} />)
  return screen.findByTestId('task-result')
}

const fixResult = (fix = { kind: 'workflow-edit', description: 'Fix the URL', edits: [{ node: 'n1', config: {} }] }) =>
  ({ summary: 's', diagnosis: 'The URL returns 500.', workflowId: 'w1', executionId: 'e1', fix })
const preview = (over = {}) => ({
  ok: true, definitionHash: 'sha256:cccc3333', workflowId: 'w1', workflowName: 'Daily digest',
  edits: [{ node: 'n1', nodeName: 'Fetch rows', path: 'url', before: '/v1/rows', after: '/v2/rows' }],
  warnings: [{ code: 'new_outbound', text: 'sends email' }, { code: 'credential_field', text: 'x' }, { code: 'expression_added', text: 'y' }, { code: 'workflow_active', text: 'z' }], ...over,
})

describe('failed-run-fix result', () => {
  it('shows the diagnosis, a before -> after diff and plain warning chips, asking for the preview once', async () => {
    aitaskApplyApi.preview.mockResolvedValue(preview())
    const line = await done('failed-run-fix', fixResult())
    await screen.findByTestId('fix-diff')
    expect(line).toHaveTextContent('The URL returns 500.')
    expect(line).toHaveTextContent('Fetch rows')
    expect(screen.getByText('/v1/rows')).toBeInTheDocument()
    expect(screen.getByText('/v2/rows')).toBeInTheDocument()
    for (const w of ['Sends, publishes or runs something', 'Changes a credential field', 'Adds an expression', 'The workflow is active']) expect(screen.getByText(w)).toBeInTheDocument()
    expect(aitaskApplyApi.preview).toHaveBeenCalledTimes(1)
    expect(aitaskApplyApi.preview).toHaveBeenCalledWith('t1')
  })

  it('collapses a long value and expands it on request', async () => {
    aitaskApplyApi.preview.mockResolvedValue(preview({ edits: [{ node: 'n1', nodeName: 'N', path: 'body', before: 'a', after: 'b'.repeat(400) }], warnings: [] }))
    await done('failed-run-fix', fixResult())
    await screen.findByTestId('fix-diff')
    expect(screen.queryByText('b'.repeat(400))).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Show more' }))
    expect(screen.getByText('b'.repeat(400))).toBeInTheDocument()
  })

  it('apply declined: confirms with the counts, applies nothing', async () => {
    aitaskApplyApi.preview.mockResolvedValue(preview())
    confirm.mockResolvedValue(false)
    await done('failed-run-fix', fixResult())
    await screen.findByTestId('fix-diff')
    fireEvent.click(screen.getByRole('button', { name: 'Apply fix' }))
    await waitFor(() => expect(confirm).toHaveBeenCalled())
    expect(confirm.mock.calls[0][0]).toMatch(/changes 1 field in 1 node of the workflow "Daily digest".*approval again before it runs/)
    expect(aitaskApplyApi.apply).not.toHaveBeenCalled()
  })

  it('apply accepted: applies, says to review and approve, and opens the workflow', async () => {
    aitaskApplyApi.preview.mockResolvedValue(preview())
    aitaskApplyApi.apply.mockResolvedValue({ ok: true, workflowId: 'w1', applied: 1, needsApproval: true })
    confirm.mockResolvedValue(true)
    const onNavigate = vi.fn()
    await done('failed-run-fix', fixResult(), onNavigate)
    await screen.findByTestId('fix-diff')
    fireEvent.click(screen.getByRole('button', { name: 'Apply fix' }))
    expect(await screen.findByText(/Applied 1 edits\. Review and approve the workflow before it runs\./)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Apply fix' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Open workflow' }))
    expect(onNavigate).toHaveBeenCalledWith('noderunner', { workflowId: 'w1' })
  })

  it('apply error: shown plainly, Apply stays available', async () => {
    aitaskApplyApi.preview.mockResolvedValue(preview())
    aitaskApplyApi.apply.mockRejectedValue(new Error('the workflow changed since the fix was proposed'))
    confirm.mockResolvedValue(true)
    await done('failed-run-fix', fixResult())
    await screen.findByTestId('fix-diff')
    fireEvent.click(screen.getByRole('button', { name: 'Apply fix' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not apply the fix: the workflow changed since the fix was proposed')
    expect(screen.getByRole('button', { name: 'Apply fix' })).toBeEnabled()
  })

  it('Apply is disabled when the preview has errors', async () => {
    aitaskApplyApi.preview.mockResolvedValue(preview({ ok: false, error: 'node n9 is gone' }))
    await done('failed-run-fix', fixResult())
    expect(await screen.findByText('node n9 is gone')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Apply fix' })).toBeDisabled()
  })

  it('a preview failure is shown and there is nothing to apply', async () => {
    aitaskApplyApi.preview.mockRejectedValue(new Error('operator only'))
    await done('failed-run-fix', fixResult())
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not check the proposed change: operator only')
    expect(screen.getByRole('button', { name: 'Apply fix' })).toBeDisabled()
  })

  it('advice and none: the description only, no preview, no Apply', async () => {
    const line = await done('failed-run-fix', fixResult({ kind: 'advice', description: 'Add a retry node' }))
    expect(line).toHaveTextContent('Add a retry node')
    expect(line).toHaveTextContent('advice only')
    expect(aitaskApplyApi.preview).not.toHaveBeenCalled()
    expect(screen.queryByRole('button', { name: 'Apply fix' })).toBeNull()
  })

  it('renders text only: no HTML, no links', async () => {
    aitaskApplyApi.preview.mockResolvedValue(preview({ warnings: [], edits: [{ node: 'n', nodeName: '<img src=x>', path: 'p', before: '<a href="http://e.test">x</a>', after: 'http://e.test' }] }))
    const line = await done('failed-run-fix', { ...fixResult(), diagnosis: '<b>bold</b> http://e.test' })
    await screen.findByTestId('fix-diff')
    expect(line.querySelector('a, img, b')).toBeNull()
  })
})

describe('workflow-generate result', () => {
  const gen = (valid = true) => ({ summary: 'Emails a digest.', workflow: { name: 'Daily digest', is_active: false, nodes: [{}, {}, {}], connections: [] }, validation: { valid, errors: valid ? [] : ['edge dangling'], warnings: ['no trigger'] } })

  it('shows the summary, node count and validation chips; Save as draft then Open workflow', async () => {
    aitaskApplyApi.saveDraft.mockResolvedValue({ ok: true, workflowId: 'w9', name: 'Daily digest', nodeCount: 3, needsApproval: true })
    const onNavigate = vi.fn()
    const line = await done('workflow-generate', gen(), onNavigate)
    expect(line).toHaveTextContent('Created Daily digest (3 nodes)')
    expect(screen.getByText('Valid')).toBeInTheDocument()
    expect(screen.getByText('1 warnings')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Save as draft' }))
    expect(await screen.findByText(/Saved as a draft: needs your approval before it can run/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Open workflow' }))
    expect(onNavigate).toHaveBeenCalledWith('noderunner', { workflowId: 'w9' })
  })

  it('a save error is shown plainly and the button stays', async () => {
    aitaskApplyApi.saveDraft.mockRejectedValue(new Error('invalid workflow'))
    await done('workflow-generate', gen())
    fireEvent.click(screen.getByRole('button', { name: 'Save as draft' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not save the draft: invalid workflow')
    expect(screen.getByRole('button', { name: 'Save as draft' })).toBeEnabled()
  })

  it('an invalid workflow shows the error chip and cannot be saved', async () => {
    await done('workflow-generate', gen(false))
    expect(screen.getByText('1 errors')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Save as draft' })).toBeDisabled()
  })
})

describe('recording-analyze result', () => {
  it('lists suggestions and shows risks as chips, with no saved/verified chips', async () => {
    const line = await done('recording-analyze', { recordingId: 'r', summary: 'Logs in.', suggestions: ['Wait for the form', 'Use a variable'], risks: ['Submits a form'] })
    expect(line).toHaveTextContent('Logs in.')
    expect(screen.getAllByRole('listitem').map(li => li.textContent)).toEqual(['Wait for the form', 'Use a variable'])
    expect(screen.getByText('Submits a form')).toBeInTheDocument()
    expect(screen.queryByText('saved')).toBeNull()
    expect(screen.queryByText('verified')).toBeNull()
  })
})

describe('missing or malformed results', () => {
  it.each([[undefined], ['text'], [['x']], [{ diagnosis: 7, fix: 'nope' }]])('keeps the header working for %j', async (result) => {
    aitaskApi.get.mockResolvedValue({ status: 'complete', kind: 'failed-run-fix', result })
    render(<TaskHeaderCard task={{ taskId: 't1', kind: 'failed-run-fix', goal: 'g', status: 'complete' }} />)
    await waitFor(() => expect(aitaskApi.get).toHaveBeenCalled())
    expect(screen.getByTestId('task-header')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Apply fix' })).toBeNull()
  })
})
