// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import React from 'react'
import { render, screen, fireEvent, waitFor, cleanup, act } from '@testing-library/react'
import '../../i18n.js'

const { aitaskApplyApi, aitaskApi, confirm } = vi.hoisted(() => ({
  aitaskApplyApi: { preview: vi.fn(), apply: vi.fn(), saveDraft: vi.fn() },
  aitaskApi: { get: vi.fn(), cancel: vi.fn(), status: vi.fn(), submit: vi.fn() },
  confirm: vi.fn(),
}))
vi.mock('../SendToCoderApi.js', async (orig) => ({ ...(await orig()), aitaskApplyApi, aitaskApi }))
vi.mock('../settings/aitaskBudgetApi.js', () => ({ aitaskBudgetApi: { forTask: vi.fn(() => Promise.resolve(null)) } }))
vi.mock('../ConfirmDialog.jsx', () => ({ confirm }))

import { TaskHeaderCard } from './TaskHeaderCard.jsx'
import { TaskResultLine } from './TaskResultLine.jsx'
import FailedRunFixResult from './FailedRunFixResult.jsx'
import { CoderBubbleContext } from './CoderBubbleContext.jsx'

const result = { diagnosis: 'd', workflowId: 'w1', executionId: 'e1', fix: { kind: 'workflow-edit', description: 'x', edits: [{ node: 'n1', config: {} }] } }
const preview = (over = {}) => ({ ok: true, definitionHash: 'sha256:aaaa1111', workflowId: 'w1', workflowName: 'WF', edits: [{ node: 'n1', nodeName: 'N', path: 'url', before: 'a', after: 'b' }], warnings: [], ...over })
const mount = (extra = {}) => render(<CoderBubbleContext.Provider value={{ openDraft: vi.fn(), navigate: vi.fn() }}><TaskResultLine kind="failed-run-fix" taskId="t1" result={result} {...extra} /></CoderBubbleContext.Provider>)
const applyBtn = () => screen.getByRole('button', { name: /Apply fix|Applying|^Applied$/ })

beforeEach(() => { confirm.mockResolvedValue(true) })
afterEach(() => { cleanup(); vi.clearAllMocks() })

describe('nothing applies without a click', () => {
  it('opening the header on a complete task only previews', async () => {
    aitaskApplyApi.preview.mockResolvedValue(preview())
    aitaskApi.get.mockResolvedValue({ status: 'complete', kind: 'failed-run-fix', result })
    render(<TaskHeaderCard task={{ taskId: 't1', kind: 'failed-run-fix', goal: 'g', status: 'complete' }} />)
    await screen.findByTestId('fix-diff')
    expect(aitaskApplyApi.apply).not.toHaveBeenCalled()
    expect(aitaskApplyApi.saveDraft).not.toHaveBeenCalled()
    expect(confirm).not.toHaveBeenCalled()
  })

  it('rendering a generated workflow never saves it', async () => {
    aitaskApi.get.mockResolvedValue({ status: 'complete', kind: 'workflow-generate', result: { summary: 's', workflow: { name: 'n', nodes: [{}] }, validation: { valid: true } } })
    render(<TaskHeaderCard task={{ taskId: 't1', kind: 'workflow-generate', goal: 'g', status: 'complete' }} />)
    await screen.findByRole('button', { name: 'Save as draft' })
    expect(aitaskApplyApi.saveDraft).not.toHaveBeenCalled()
  })
})

describe('Apply is disabled', () => {
  it('while the preview loads, and a click calls nothing', async () => {
    aitaskApplyApi.preview.mockReturnValue(new Promise(() => {}))
    mount()
    expect(applyBtn()).toBeDisabled()
    fireEvent.click(applyBtn())
    expect(confirm).not.toHaveBeenCalled()
    expect(aitaskApplyApi.apply).not.toHaveBeenCalled()
  })

  it('when the preview has errors, and a click calls nothing', async () => {
    aitaskApplyApi.preview.mockResolvedValue(preview({ ok: false, error: 'node gone' }))
    mount()
    await screen.findByText('node gone')
    fireEvent.click(applyBtn())
    expect(confirm).not.toHaveBeenCalled()
    expect(aitaskApplyApi.apply).not.toHaveBeenCalled()
  })

  it('while an apply is in flight: a second click does not apply twice', async () => {
    aitaskApplyApi.preview.mockResolvedValue(preview())
    let finish
    aitaskApplyApi.apply.mockReturnValue(new Promise(r => { finish = r }))
    mount()
    await screen.findByTestId('fix-diff')
    fireEvent.click(applyBtn())
    await waitFor(() => expect(aitaskApplyApi.apply).toHaveBeenCalledTimes(1))
    expect(applyBtn()).toBeDisabled()
    fireEvent.click(applyBtn())
    finish({ ok: true, applied: 1, workflowId: 'w1' })
    await screen.findByText(/Applied 1 edits/)
    expect(aitaskApplyApi.apply).toHaveBeenCalledTimes(1)
    expect(confirm).toHaveBeenCalledTimes(1)
  })

  it('while the confirm dialog is open: a second click does not open another', async () => {
    aitaskApplyApi.preview.mockResolvedValue(preview())
    let answer
    confirm.mockReturnValue(new Promise(r => { answer = r }))
    mount()
    await screen.findByTestId('fix-diff')
    fireEvent.click(applyBtn())
    fireEvent.click(applyBtn())
    expect(confirm).toHaveBeenCalledTimes(1)
    answer(false)
    await waitFor(() => expect(applyBtn()).toBeEnabled())
    expect(aitaskApplyApi.apply).not.toHaveBeenCalled()
  })
})

describe('warning codes', () => {
  it('an unknown code shows the CLI text plainly', async () => {
    aitaskApplyApi.preview.mockResolvedValue(preview({ warnings: [{ code: 'something_new', text: 'The CLI says hello' }] }))
    mount()
    expect(await screen.findByText('The CLI says hello')).toBeInTheDocument()
  })
})

describe('the reviewed hash and size limits', () => {
  it('passes back the hash of the preview that was shown, and keeps Apply disabled without one', async () => {
    aitaskApplyApi.preview.mockResolvedValue(preview({ definitionHash: undefined }))
    mount()
    await screen.findByTestId('fix-diff')
    expect(applyBtn()).toBeDisabled()
    cleanup()
    aitaskApplyApi.preview.mockResolvedValue(preview({ definitionHash: 'sha256:bbbb2222' }))
    aitaskApplyApi.apply.mockResolvedValue({ ok: true, applied: 1, workflowId: 'w1' })
    mount()
    await screen.findByTestId('fix-diff')
    fireEvent.click(applyBtn())
    await screen.findByText(/Applied 1 edits/)
    expect(aitaskApplyApi.apply).toHaveBeenCalledWith('t1', false, 'sha256:bbbb2222')
  })

  it('changed_since_preview: says nothing was applied, and Review again loads a fresh preview whose hash is used', async () => {
    aitaskApplyApi.preview.mockResolvedValueOnce(preview({ definitionHash: 'sha256:old00001' })).mockResolvedValueOnce(preview({ definitionHash: 'sha256:new00002' }))
    aitaskApplyApi.apply.mockRejectedValueOnce(new Error('changed_since_preview: differs')).mockResolvedValueOnce({ ok: true, applied: 1, workflowId: 'w1' })
    mount()
    await screen.findByTestId('fix-diff')
    fireEvent.click(applyBtn())
    expect(await screen.findByRole('alert')).toHaveTextContent('The workflow changed after you reviewed this: nothing was applied. Review it again.')
    fireEvent.click(screen.getByRole('button', { name: 'Review again' }))
    await waitFor(() => expect(aitaskApplyApi.preview).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(applyBtn()).toBeEnabled())
    fireEvent.click(applyBtn())
    await screen.findByText(/Applied 1 edits/)
    expect(aitaskApplyApi.apply.mock.calls.map(c => c[2])).toEqual(['sha256:old00001', 'sha256:new00002'])
  })

  it('too_large: the plain sentence and Ask again; no_target: the CLI text', async () => {
    aitaskApplyApi.preview.mockResolvedValue(preview())
    aitaskApplyApi.apply.mockRejectedValueOnce(new Error('too_large: big'))
    mount()
    await screen.findByTestId('fix-diff')
    fireEvent.click(applyBtn())
    expect(await screen.findByRole('alert')).toHaveTextContent('This proposal is bigger than the review can show: ask again for a smaller change.')
    expect(screen.getByRole('button', { name: /Ask again/ })).toBeInTheDocument()
    cleanup()
    aitaskApplyApi.apply.mockRejectedValueOnce(new Error('no_target: the node n9 does not exist'))
    mount()
    await screen.findByTestId('fix-diff')
    fireEvent.click(applyBtn())
    expect(await screen.findByRole('alert')).toHaveTextContent('the node n9 does not exist')
  })

  it('shows every changed leaf (no cut-off at 50) and a full 2000-character value behind Show more', async () => {
    const edits = Array.from({ length: 50 }, (_, i) => ({ node: `n${i}`, nodeName: `Node ${i}`, path: 'p', before: 'a', after: i === 49 ? 'z'.repeat(2000) : 'b' }))
    aitaskApplyApi.preview.mockResolvedValue(preview({ edits }))
    mount()
    const diff = await screen.findByTestId('fix-diff')
    expect(diff.querySelectorAll('li')).toHaveLength(50)
    expect(screen.queryByText('z'.repeat(2000))).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Show more' }))
    expect(screen.getByText('z'.repeat(2000))).toBeInTheDocument()
    expect(applyBtn()).toBeEnabled()
  })

  it('more than 50 leaves, or a value over 2000 characters: Apply is disabled and the review says it cannot show it', async () => {
    const many = Array.from({ length: 51 }, (_, i) => ({ node: `n${i}`, nodeName: 'N', path: 'p', before: 'a', after: 'b' }))
    aitaskApplyApi.preview.mockResolvedValue(preview({ edits: many }))
    mount()
    await screen.findByTestId('fix-diff')
    expect(screen.getByRole('alert')).toHaveTextContent('bigger than the review can show')
    expect(applyBtn()).toBeDisabled()
    cleanup()
    aitaskApplyApi.preview.mockResolvedValue(preview({ edits: [{ node: 'n', nodeName: 'N', path: 'p', before: 'a', after: 'c'.repeat(2001) }] }))
    mount()
    await screen.findByTestId('fix-diff')
    expect(applyBtn()).toBeDisabled()
    fireEvent.click(applyBtn())
    expect(aitaskApplyApi.apply).not.toHaveBeenCalled()
  })
})

const withTask = (id, extra = {}) => <CoderBubbleContext.Provider value={{ openDraft: vi.fn(), navigate: vi.fn() }}><FailedRunFixResult taskId={id} result={result} {...extra} /></CoderBubbleContext.Provider>

describe('a preview is bound to its task', () => {
  it('switching A to B while B is pending: A is not shown, Apply is disabled, nothing is called', async () => {
    aitaskApplyApi.preview.mockResolvedValueOnce(preview({ definitionHash: 'sha256:hashAAAA' })).mockReturnValueOnce(new Promise(() => {}))
    const { rerender } = render(withTask('tA'))
    await screen.findByTestId('fix-diff')
    expect(applyBtn()).toBeEnabled()
    rerender(withTask('tB'))
    expect(screen.queryByTestId('fix-diff')).toBeNull()
    expect(applyBtn()).toBeDisabled()
    fireEvent.click(applyBtn())
    expect(confirm).not.toHaveBeenCalled()
    expect(aitaskApplyApi.apply).not.toHaveBeenCalled()
  })

  it("B's preview fails after the switch: Apply stays disabled", async () => {
    aitaskApplyApi.preview.mockResolvedValueOnce(preview()).mockRejectedValueOnce(new Error('boom'))
    const { rerender } = render(withTask('tA'))
    await screen.findByTestId('fix-diff')
    rerender(withTask('tB'))
    await screen.findByText(/Could not check the proposed change: boom/)
    expect(screen.queryByTestId('fix-diff')).toBeNull()
    expect(applyBtn()).toBeDisabled()
  })

  it("A's late reply after the switch is ignored", async () => {
    let lateA
    aitaskApplyApi.preview.mockReturnValueOnce(new Promise(r => { lateA = r })).mockReturnValueOnce(new Promise(() => {}))
    const { rerender } = render(withTask('tA'))
    rerender(withTask('tB'))
    await act(async () => { lateA(preview({ definitionHash: 'sha256:hashAAAA' })) })
    expect(screen.queryByTestId('fix-diff')).toBeNull()
    expect(applyBtn()).toBeDisabled()
  })

  it('two proposals on the same workflow never cross hashes', async () => {
    aitaskApplyApi.preview.mockResolvedValueOnce(preview({ definitionHash: 'sha256:hashAAAA' })).mockResolvedValueOnce(preview({ definitionHash: 'sha256:hashBBBB', edits: [{ node: 'n2', nodeName: 'Other', path: 'x', before: '1', after: '2' }] }))
    aitaskApplyApi.apply.mockResolvedValue({ ok: true, applied: 1, workflowId: 'w1' })
    const { rerender } = render(withTask('tA'))
    await screen.findByTestId('fix-diff')
    rerender(withTask('tB'))
    await screen.findByText('Other')
    fireEvent.click(applyBtn())
    await screen.findByText(/Applied 1 edits/)
    expect(aitaskApplyApi.apply).toHaveBeenCalledTimes(1)
    expect(aitaskApplyApi.apply).toHaveBeenCalledWith('tB', false, 'sha256:hashBBBB')
  })

  it('an apply of A that answers after the switch to B does not mark B applied', async () => {
    aitaskApplyApi.preview.mockResolvedValueOnce(preview()).mockReturnValueOnce(new Promise(() => {}))
    let finish
    aitaskApplyApi.apply.mockReturnValue(new Promise(r => { finish = r }))
    const { rerender } = render(withTask('tA'))
    await screen.findByTestId('fix-diff')
    fireEvent.click(applyBtn())
    await waitFor(() => expect(aitaskApplyApi.apply).toHaveBeenCalled())
    rerender(withTask('tB'))
    await act(async () => { finish({ ok: true, applied: 1, workflowId: 'w1' }) })
    expect(screen.queryByText(/Applied 1 edits/)).toBeNull()
  })

  it('TaskResultLine keys the result by task, so the state resets', async () => {
    aitaskApplyApi.preview.mockResolvedValueOnce(preview()).mockReturnValueOnce(new Promise(() => {}))
    const wrap = (id) => <CoderBubbleContext.Provider value={{ openDraft: vi.fn(), navigate: vi.fn() }}><TaskResultLine kind="failed-run-fix" taskId={id} result={result} /></CoderBubbleContext.Provider>
    const { rerender } = render(wrap('tA'))
    await screen.findByTestId('fix-diff')
    rerender(wrap('tB'))
    expect(screen.queryByTestId('fix-diff')).toBeNull()
    expect(applyBtn()).toBeDisabled()
  })
})

describe('every apply call re-verifies', () => {
  const activePreview = (hash = 'sha256:hashAAAA') => preview({ definitionHash: hash, warnings: [{ code: 'workflow_active', text: 'active' }] })

  it('switching tasks while the active-workflow confirm is open, then confirming: no binding call', async () => {
    aitaskApplyApi.preview.mockResolvedValueOnce(activePreview()).mockReturnValueOnce(new Promise(() => {}))
    let answerActive
    confirm.mockResolvedValueOnce(true).mockReturnValueOnce(new Promise(r => { answerActive = r }))
    const { rerender } = render(withTask('tA'))
    await screen.findByTestId('fix-diff')
    fireEvent.click(applyBtn())
    await waitFor(() => expect(confirm).toHaveBeenCalledTimes(2))
    rerender(withTask('tB'))
    await act(async () => { answerActive(true) })
    expect(aitaskApplyApi.apply).not.toHaveBeenCalled()
    expect(applyBtn()).toBeDisabled()
  })

  it('the retry after a workflow_active_needs_confirm refusal is dropped when the task changed meanwhile', async () => {
    aitaskApplyApi.preview.mockResolvedValueOnce(preview()).mockReturnValueOnce(new Promise(() => {}))
    aitaskApplyApi.apply.mockRejectedValueOnce(new Error('workflow_active_needs_confirm: running'))
    let answerActive
    confirm.mockResolvedValueOnce(true).mockReturnValueOnce(new Promise(r => { answerActive = r }))
    const { rerender } = render(withTask('tA'))
    await screen.findByTestId('fix-diff')
    fireEvent.click(applyBtn())
    await waitFor(() => expect(confirm).toHaveBeenCalledTimes(2))
    rerender(withTask('tB'))
    await act(async () => { answerActive(true) })
    expect(aitaskApplyApi.apply).toHaveBeenCalledTimes(1) // only the first, refused call
  })

  it('Review again, then Apply: only the NEW hash is ever sent', async () => {
    aitaskApplyApi.preview.mockResolvedValueOnce(preview({ definitionHash: 'sha256:old00001' })).mockResolvedValueOnce(preview({ definitionHash: 'sha256:new00002' }))
    aitaskApplyApi.apply.mockRejectedValueOnce(new Error('changed_since_preview: x')).mockResolvedValueOnce({ ok: true, applied: 1, workflowId: 'w1' })
    mount()
    await screen.findByTestId('fix-diff')
    fireEvent.click(applyBtn())
    await screen.findByRole('button', { name: 'Review again' })
    fireEvent.click(screen.getByRole('button', { name: 'Review again' }))
    await waitFor(() => expect(applyBtn()).toBeEnabled())
    fireEvent.click(applyBtn())
    await screen.findByText(/Applied 1 edits/)
    expect(aitaskApplyApi.apply.mock.calls.map(c => c[2])).toEqual(['sha256:old00001', 'sha256:new00002'])
  })

  it('StrictMode makes exactly one preview request and one apply', async () => {
    aitaskApplyApi.preview.mockResolvedValue(preview())
    aitaskApplyApi.apply.mockResolvedValue({ ok: true, applied: 1, workflowId: 'w1' })
    render(<React.StrictMode>{withTask('tA')}</React.StrictMode>)
    await screen.findByTestId('fix-diff')
    expect(aitaskApplyApi.preview).toHaveBeenCalledTimes(1)
    fireEvent.click(applyBtn())
    fireEvent.click(applyBtn())
    await screen.findByText(/Applied 1 edits/)
    expect(aitaskApplyApi.apply).toHaveBeenCalledTimes(1)
  })

  it('invalid_key and file_sync_failed show the CLI text plainly', async () => {
    aitaskApplyApi.preview.mockResolvedValue(preview())
    aitaskApplyApi.apply.mockRejectedValueOnce(new Error('invalid_key: key "a\u202eb" has hidden characters'))
    mount()
    await screen.findByTestId('fix-diff')
    fireEvent.click(applyBtn())
    expect(await screen.findByRole('alert')).toHaveTextContent('has hidden characters')
    cleanup()
    aitaskApplyApi.apply.mockRejectedValueOnce(new Error('file_sync_failed: the file could not be synced (file_inconsistent)'))
    mount()
    await screen.findByTestId('fix-diff')
    fireEvent.click(applyBtn())
    expect(await screen.findByRole('alert')).toHaveTextContent('file_inconsistent')
  })
})

describe('visual QA fixes', () => {
  it('D3: after changed_since_preview Apply stays disabled until Review again loads a fresh preview', async () => {
    aitaskApplyApi.preview.mockResolvedValueOnce(preview({ definitionHash: 'sha256:old00001' })).mockResolvedValueOnce(preview({ definitionHash: 'sha256:new00002' }))
    aitaskApplyApi.apply.mockRejectedValueOnce(new Error('changed_since_preview: x'))
    mount()
    await screen.findByTestId('fix-diff')
    fireEvent.click(applyBtn())
    await screen.findByRole('button', { name: 'Review again' })
    expect(applyBtn()).toBeDisabled()
    fireEvent.click(applyBtn())
    expect(aitaskApplyApi.apply).toHaveBeenCalledTimes(1)
    fireEvent.click(screen.getByRole('button', { name: 'Review again' }))
    await waitFor(() => expect(applyBtn()).toBeEnabled())
  })

  it('D4: the apply confirm uses proper plurals', async () => {
    aitaskApplyApi.preview.mockResolvedValue(preview())
    mount()
    await screen.findByTestId('fix-diff')
    fireEvent.click(applyBtn())
    await waitFor(() => expect(confirm).toHaveBeenCalled())
    expect(confirm.mock.calls[0][0]).toContain('changes 1 field in 1 node of the workflow')
    cleanup(); confirm.mockClear()
    aitaskApplyApi.preview.mockResolvedValue(preview({ edits: [
      { node: 'a', nodeName: 'A', path: 'p1', before: '1', after: '2' }, { node: 'a', nodeName: 'A', path: 'p2', before: '1', after: '2' }, { node: 'b', nodeName: 'B', path: 'p3', before: '1', after: '2' }] }))
    mount()
    await screen.findByTestId('fix-diff')
    fireEvent.click(applyBtn())
    await waitFor(() => expect(confirm).toHaveBeenCalled())
    expect(confirm.mock.calls[0][0]).toContain('changes 3 fields in 2 nodes of the workflow')
  })

  it('D5: Apply has its own row, away from the warning chips, and the confirm is not styled as destructive', async () => {
    aitaskApplyApi.preview.mockResolvedValue(preview({ warnings: [{ code: 'new_outbound', text: 'x' }] }))
    mount()
    await screen.findByTestId('fix-diff')
    const row = screen.getByTestId('apply-row')
    expect(row).toContainElement(applyBtn())
    expect(row).not.toHaveTextContent('Sends, publishes or runs something')
    fireEvent.click(applyBtn())
    await waitFor(() => expect(confirm).toHaveBeenCalled())
    expect(confirm.mock.calls[0][1].danger).toBe(false)
  })

  it('D6: credential-looking keys are masked, and Show more cannot reveal them', async () => {
    aitaskApplyApi.preview.mockResolvedValue(preview({ edits: [
      { node: 'n', nodeName: 'HTTP', path: 'credentials.apiKey', before: 'old-key-VALUE-1', after: 'new-key-VALUE-2' + 'x'.repeat(300) },
      { node: 'n', nodeName: 'HTTP', path: 'auth.token', before: 'tok-OLD', after: 'tok-NEW' },
      { node: 'n', nodeName: 'HTTP', path: 'credential_id', before: 'cred-1', after: 'cred-2' }] }))
    mount()
    const diff = await screen.findByTestId('fix-diff')
    expect(diff.textContent).not.toMatch(/VALUE|tok-OLD|tok-NEW/)
    expect(diff.textContent.match(/••••••/g)).toHaveLength(4)
    expect(screen.getAllByText(/hidden: looks like a credential/).length).toBeGreaterThanOrEqual(2)
    expect(screen.queryByRole('button', { name: 'Show more' })).toBeNull()
    expect(diff.textContent).toContain('cred-1') // credential_id is not a secret
  })
})

describe('looksLikeCredential', () => {
  it.each([['credentials.apiKey', true], ['auth.token', true], ['headers.Authorization', true], ['x.password', true], ['a.client_secret', true], ['Cookie', true], ['otp', true], ['card.cvv', true], ['private_key', true], ['access-key', true], ['api_key', true], ['credential_id', false], ['url', false], ['parameters.footprint', false], ['hotpath.x', false]])('%s -> %s', async (path, want) => {
    const { looksLikeCredential } = await import('./FailedRunFixResult.jsx')
    expect(looksLikeCredential(path)).toBe(want)
  })
})

describe('full paths', () => {
  it('shows a long path in full, without an ellipsis', async () => {
    const path = 'parameters.' + 'segment.'.repeat(60) + 'leaf'
    aitaskApplyApi.preview.mockResolvedValue(preview({ edits: [{ node: 'n', nodeName: 'N', path, before: 'a', after: 'b' }] }))
    mount()
    const diff = await screen.findByTestId('fix-diff')
    expect(diff.textContent).toContain(path)
    expect(diff.textContent).not.toContain('…')
    expect(applyBtn()).toBeEnabled()
  })

  it('a path over 512 characters disables Apply and says the review cannot show it', async () => {
    aitaskApplyApi.preview.mockResolvedValue(preview({ edits: [{ node: 'n', nodeName: 'N', path: 'p'.repeat(513), before: 'a', after: 'b' }] }))
    mount()
    await screen.findByTestId('fix-diff')
    expect(screen.getByRole('alert')).toHaveTextContent('bigger than the review can show')
    expect(applyBtn()).toBeDisabled()
  })
})

describe('typed refusals', () => {
  const clickApply = async () => { await screen.findByTestId('fix-diff'); fireEvent.click(applyBtn()) }

  it('stale_proposal: says to ask again and offers the Fix with coder flow', async () => {
    aitaskApplyApi.preview.mockResolvedValue(preview())
    aitaskApplyApi.apply.mockRejectedValue(new Error('stale_proposal: the workflow differs'))
    mount()
    await clickApply()
    expect(await screen.findByRole('alert')).toHaveTextContent('The workflow changed since this was proposed: ask again.')
    aitaskApi.status.mockResolvedValue({ enabled: true })
    aitaskApi.submit.mockResolvedValue({ taskId: 't2', conversationId: 'c2', status: 'queued', goal: 'g' })
    fireEvent.click(screen.getByRole('button', { name: /Ask again/ }))
    await waitFor(() => expect(aitaskApi.submit).toHaveBeenCalled())
    expect(aitaskApi.submit.mock.calls[0][0]).toBe('failed-run-fix')
    expect(aitaskApi.submit.mock.calls[0][2]).toEqual(['selection-text=workflow: w1\nexecution: e1'])
  })

  it('already_applied: shows Applied, disabled', async () => {
    aitaskApplyApi.preview.mockResolvedValue(preview())
    aitaskApplyApi.apply.mockRejectedValue(new Error('already_applied: done'))
    mount()
    await clickApply()
    await waitFor(() => expect(applyBtn()).toHaveTextContent('Applied'))
    expect(applyBtn()).toBeDisabled()
  })

  it('operator_only: plain wording', async () => {
    aitaskApplyApi.preview.mockResolvedValue(preview())
    aitaskApplyApi.apply.mockRejectedValue(new Error('operator_only: nope'))
    mount()
    await clickApply()
    expect(await screen.findByRole('alert')).toHaveTextContent('Only you can apply this, from the app window.')
  })

  it('an active workflow: a second confirm, and the allow flag only after it', async () => {
    aitaskApplyApi.preview.mockResolvedValue(preview({ warnings: [{ code: 'workflow_active', text: 'active' }] }))
    aitaskApplyApi.apply.mockResolvedValue({ ok: true, applied: 1, workflowId: 'w1' })
    mount()
    await clickApply()
    await screen.findByText(/Applied 1 edits/)
    expect(confirm).toHaveBeenCalledTimes(2)
    expect(confirm.mock.calls[1][0]).toMatch(/running or scheduled/)
    expect(aitaskApplyApi.apply).toHaveBeenCalledWith('t1', true, 'sha256:aaaa1111')
  })

  it('an active workflow, second confirm declined: nothing is applied', async () => {
    aitaskApplyApi.preview.mockResolvedValue(preview({ warnings: [{ code: 'workflow_active', text: 'active' }] }))
    confirm.mockResolvedValueOnce(true).mockResolvedValueOnce(false)
    mount()
    await clickApply()
    await waitFor(() => expect(confirm).toHaveBeenCalledTimes(2))
    expect(aitaskApplyApi.apply).not.toHaveBeenCalled()
  })

  it('a refusal for an active workflow the preview did not flag: asks, then retries with the flag', async () => {
    aitaskApplyApi.preview.mockResolvedValue(preview())
    aitaskApplyApi.apply.mockRejectedValueOnce(new Error('workflow_active_needs_confirm: it is running')).mockResolvedValueOnce({ ok: true, applied: 1, workflowId: 'w1' })
    mount()
    await clickApply()
    await screen.findByText(/Applied 1 edits/)
    expect(aitaskApplyApi.apply.mock.calls).toEqual([['t1', false, 'sha256:aaaa1111'], ['t1', true, 'sha256:aaaa1111']])
  })

  it('a normal apply never sends the allow flag', async () => {
    aitaskApplyApi.preview.mockResolvedValue(preview())
    aitaskApplyApi.apply.mockResolvedValue({ ok: true, applied: 1, workflowId: 'w1' })
    mount()
    await clickApply()
    await screen.findByText(/Applied 1 edits/)
    expect(aitaskApplyApi.apply).toHaveBeenCalledWith('t1', false, 'sha256:aaaa1111')
  })
})
