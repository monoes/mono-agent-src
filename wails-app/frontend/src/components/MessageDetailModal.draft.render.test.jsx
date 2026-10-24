// @vitest-environment jsdom
// "Draft with coder" on the message view: only text the user selected or
// pasted is handed over (a draft-reply task never gets the synced message).
import React from 'react'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
import '../i18n.js'

const { aitaskApi } = vi.hoisted(() => ({
  aitaskApi: { status: vi.fn(), submit: vi.fn(), startFill: vi.fn(), get: vi.fn(), cancel: vi.fn() },
}))
vi.mock('./SendToCoderApi.js', async (orig) => ({ ...(await orig()), aitaskApi }))

import MessageDetailModal from './MessageDetailModal.jsx'
import { CoderBubbleContext } from './bubbles/CoderBubbleContext.jsx'

const message = { id: 'm1', source: 'outlook', subject: 'Call', body: 'Hi, can we move the call to Friday? Secret-ish chatter.', sent_at: '2026-10-01T10:00:00Z' }

function renderModal(msg = message) {
  const ctx = { openDraft: vi.fn(), navigate: vi.fn() }
  render(
    <CoderBubbleContext.Provider value={ctx}>
      <MessageDetailModal message={msg} personLabel="Sam" onClose={vi.fn()} />
    </CoderBubbleContext.Provider>,
  )
  return ctx
}

beforeEach(() => { vi.clearAllMocks() })
afterEach(() => { cleanup() })

describe('MessageDetailModal draft with coder', () => {
  it('offers nothing until the user has selected or pasted text', () => {
    renderModal()
    expect(screen.queryByRole('button', { name: /draft with coder/i })).toBeNull()
  })

  it('submits kind draft-reply with only the pasted text as selection context, never the message', async () => {
    aitaskApi.status.mockResolvedValue({ enabled: true })
    aitaskApi.submit.mockResolvedValue({ taskId: 't1', conversationId: 'c1', status: 'running', kind: 'draft-reply' })
    const ctx = renderModal()
    fireEvent.change(screen.getByLabelText('Text to reply to'), { target: { value: 'can we move the call to Friday?' } })
    fireEvent.click(screen.getByRole('button', { name: /draft with coder/i }))
    await waitFor(() => expect(ctx.openDraft).toHaveBeenCalledTimes(1))
    const [kind, goal, context] = aitaskApi.submit.mock.calls[0]
    expect(kind).toBe('draft-reply')
    expect(context).toEqual(['selection-text=can we move the call to Friday?'])
    expect(goal).toMatch(/do not send/i)
    expect(`${goal}${JSON.stringify(context)}`).not.toContain('Secret-ish')
    expect(ctx.openDraft.mock.calls[0][0].task.kind).toBe('draft-reply')
  })

  it('"Use selection" copies the selected part of a plain-text body', () => {
    renderModal()
    const pre = screen.getByText(/can we move the call/)
    const range = document.createRange()
    range.selectNodeContents(pre)
    window.getSelection().removeAllRanges()
    window.getSelection().addRange(range)
    fireEvent.click(screen.getByRole('button', { name: /use selection/i }))
    expect(screen.getByLabelText('Text to reply to').value).toContain('move the call to Friday')
  })

  it('coder off: says why, submits nothing, and the message stays readable', async () => {
    aitaskApi.status.mockResolvedValue({ enabled: false, reason: 'Coder mode is off.' })
    renderModal()
    fireEvent.change(screen.getByLabelText('Text to reply to'), { target: { value: 'reply to this' } })
    fireEvent.click(screen.getByRole('button', { name: /draft with coder/i }))
    expect(await screen.findByText(/Coder mode is off\./)).toBeInTheDocument()
    expect(aitaskApi.submit).not.toHaveBeenCalled()
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })

  it('an HTML body has no "Use selection" (the sandboxed frame cannot be read), paste still works', () => {
    renderModal({ ...message, body: '<p>Hello</p>' })
    expect(screen.queryByRole('button', { name: /use selection/i })).toBeNull()
    expect(screen.getByLabelText('Text to reply to')).toBeInTheDocument()
  })
})
