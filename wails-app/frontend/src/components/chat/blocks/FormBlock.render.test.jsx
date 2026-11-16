// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import '../../../i18n.js'

vi.mock('../../../services/api.js', () => ({ api: { openURL: vi.fn(), classifyChatPath: vi.fn(), resolveChatPath: vi.fn() }, notify: vi.fn() }))
import { ChatMarkdown } from '../ChatMarkdown.jsx'
import { ChatBlocksHost, ChatBlocksScope } from './context.js'
import { validateForm, formErrors, formMessage } from './formSchema.js'
import { parseBlock } from './parse.js'

beforeEach(() => localStorage.clear())
afterEach(cleanup)
const fence = (o) => `\`\`\`form\n${JSON.stringify(o)}\n\`\`\``
const spec = {
  title: 'Schedule',
  fields: [
    { id: 'name', type: 'text', label: 'Name', required: true },
    { id: 'count', type: 'number', label: 'Count', min: 1, max: 5, default: 2 },
    { id: 'mode', type: 'select', label: 'Mode', options: [{ value: 'a', label: 'Alpha' }, { value: 'b', label: 'Beta' }] },
    { id: 'notify', type: 'checkbox', label: 'Notify me' },
  ],
}
const view = (onSend) => (
  <ChatBlocksHost.Provider value={{ onSend, conversationId: 'c1' }}>
    <ChatBlocksScope.Provider value={{ scope: 't:p', live: false }}><ChatMarkdown content={fence(spec)} /></ChatBlocksScope.Provider>
  </ChatBlocksHost.Provider>
)

describe('form schema', () => {
  const st = (o) => parseBlock('form', JSON.stringify(o), { form: validateForm }).status
  it('validates', () => {
    expect(st(spec)).toBe('ok')
    expect(st({ fields: [] })).toBe('invalid')
    expect(st({ fields: [{ id: 'a', type: 'text', label: 'A' }, { id: 'a', type: 'text', label: 'B' }] })).toBe('invalid')
    expect(st({ fields: [{ id: '1a', type: 'text', label: 'A' }] })).toBe('invalid')
    expect(st({ fields: [{ id: 'a', type: 'radio', label: 'A' }] })).toBe('invalid')
    expect(st({ fields: [{ id: 'a', type: 'select', label: 'A', options: [{ value: 'x', label: 'X' }] }] })).toBe('invalid')
    expect(st({ fields: [{ id: 'a', type: 'select', label: 'A', default: 'z', options: [{ value: 'x', label: 'X' }, { value: 'y', label: 'Y' }] }] })).toBe('invalid')
    expect(st({ fields: [{ id: 'a', type: 'number', label: 'A', default: 'x' }] })).toBe('invalid')
    expect(st({ fields: Array.from({ length: 13 }, (_, i) => ({ id: `f${i}`, type: 'text', label: 'A' })) })).toBe('invalid')
  })
  it('checks values and builds the message', () => {
    const d = validateForm(spec)
    expect(formErrors(d.fields, { name: '', count: '9', mode: '', notify: false })).toEqual({ name: 'required', count: 'range' })
    expect(formErrors(d.fields, { name: 'x', count: 'abc', mode: '', notify: false })).toEqual({ count: 'number' })
    expect(formMessage(d, { name: 'x\ny', count: '3', mode: 'b', notify: true })).toBe('Schedule\nName: x y\nCount: 3\nMode: Beta\nNotify me: yes')
  })
})

describe('FormBlock', () => {
  it('blocks an invalid submit, then sends the values and locks', async () => {
    const onSend = vi.fn().mockResolvedValue(true)
    render(view(onSend))
    fireEvent.click(screen.getByRole('button', { name: 'Submit' }))
    expect(onSend).not.toHaveBeenCalled()
    expect(screen.getByRole('alert')).toHaveTextContent('Required')
    fireEvent.change(screen.getByLabelText(/Name/), { target: { value: 'Nightly' } })
    fireEvent.change(screen.getByLabelText('Mode'), { target: { value: 'b' } })
    fireEvent.click(screen.getByLabelText('Notify me'))
    fireEvent.click(screen.getByRole('button', { name: 'Submit' }))
    await waitFor(() => expect(onSend).toHaveBeenCalledWith('Schedule\nName: Nightly\nCount: 2\nMode: Beta\nNotify me: yes'))
    await waitFor(() => expect(screen.getByText('Answered')).toBeInTheDocument())
    expect(screen.getByLabelText(/Name/)).toBeDisabled()
    expect(screen.getByLabelText(/Name/)).toHaveValue('Nightly')
  })
  it('renders disabled with a note when it cannot send', () => {
    render(view(undefined))
    expect(screen.getByLabelText(/Name/)).toBeDisabled()
    expect(screen.getByText(/not available/i)).toBeInTheDocument()
  })
})
