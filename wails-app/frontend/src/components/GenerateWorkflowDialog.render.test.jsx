// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'

const { aitaskApi } = vi.hoisted(() => ({ aitaskApi: { status: vi.fn(), submit: vi.fn() } }))
vi.mock('./SendToCoderApi.js', async (orig) => ({ ...(await orig()), aitaskApi }))

import '../i18n.js'
import GenerateWorkflowDialog, { BRIEF_MAX } from './GenerateWorkflowDialog.jsx'
import { CoderBubbleContext } from './bubbles/CoderBubbleContext.jsx'

const openDraft = vi.fn()
const navigate = vi.fn()
const onClose = vi.fn()
const renderDialog = () => render(
  <CoderBubbleContext.Provider value={{ openDraft, navigate }}><GenerateWorkflowDialog onClose={onClose} /></CoderBubbleContext.Provider>,
)
beforeEach(() => { vi.clearAllMocks() })
afterEach(cleanup)

describe('GenerateWorkflowDialog', () => {
  it('needs a brief, counts it, and caps it at 4000', () => {
    renderDialog()
    expect(screen.getByRole('button', { name: /Generate with coder/ })).toBeDisabled()
    const box = screen.getByLabelText(/Describe what the workflow/)
    expect(box).toHaveAttribute('maxlength', String(BRIEF_MAX))
    fireEvent.change(box, { target: { value: 'hello' } })
    expect(screen.getByTestId('brief-count')).toHaveTextContent('5 / 4000')
    expect(screen.getByRole('button', { name: /Generate with coder/ })).toBeEnabled()
  })

  it('submits workflow-generate with the brief as selection text, then closes', async () => {
    aitaskApi.status.mockResolvedValue({ enabled: true })
    aitaskApi.submit.mockResolvedValue({ taskId: 't1', conversationId: 'c1', status: 'queued', goal: 'g', kind: 'workflow-generate' })
    renderDialog()
    fireEvent.change(screen.getByLabelText(/Describe what the workflow/), { target: { value: '  email me daily  ' } })
    fireEvent.click(screen.getByRole('button', { name: /Generate with coder/ }))
    await waitFor(() => expect(onClose).toHaveBeenCalled())
    expect(aitaskApi.submit.mock.calls[0][0]).toBe('workflow-generate')
    expect(aitaskApi.submit.mock.calls[0][2]).toEqual(['selection-text=email me daily'])
    expect(openDraft.mock.calls[0][0].task.kind).toBe('workflow-generate')
  })

  it('coder off: says why with a Settings link, no non-coder path, no submit', async () => {
    aitaskApi.status.mockResolvedValue({ enabled: false, reason: 'Coder mode is off.' })
    renderDialog()
    fireEvent.change(screen.getByLabelText(/Describe what the workflow/), { target: { value: 'x' } })
    fireEvent.click(screen.getByRole('button', { name: /Generate with coder/ }))
    expect(await screen.findByText(/Coder mode is off\./)).toBeInTheDocument()
    expect(screen.queryByText(/still runs the regular way/)).toBeNull()
    fireEvent.click(screen.getByText('Open Settings'))
    expect(navigate).toHaveBeenCalledWith('settings')
    expect(aitaskApi.submit).not.toHaveBeenCalled()
    expect(onClose).not.toHaveBeenCalled()
  })
})
