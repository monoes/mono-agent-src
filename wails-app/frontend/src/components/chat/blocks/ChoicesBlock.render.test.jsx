// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import '../../../i18n.js'

vi.mock('../../../services/api.js', () => ({ api: { openURL: vi.fn(), classifyChatPath: vi.fn(), resolveChatPath: vi.fn() }, notify: vi.fn() }))
import { ChatMarkdown } from '../ChatMarkdown.jsx'
import { ChatBlocksHost, ChatBlocksScope } from './context.js'
import { validateChoices } from './choicesSchema.js'
import { parseBlock } from './parse.js'

beforeEach(() => localStorage.clear())
afterEach(cleanup)

const json = (o) => JSON.stringify(o)
const doc = (o, extra = '') => `Pick one\n\n\`\`\`choices\n${typeof o === 'string' ? o : json(o)}\n\`\`\`${extra}`
const spec = { question: 'Which branch?', options: [{ id: 'a', label: 'main' }, { id: 'b', label: 'dev', hint: 'unstable' }] }

function view(content, { onSend, live = false, scope = 't1:p1', conversationId = 'c1' } = {}) {
  return (
    <ChatBlocksHost.Provider value={{ onSend, conversationId }}>
      <ChatBlocksScope.Provider value={{ scope, live }}>
        <ChatMarkdown content={content} />
      </ChatBlocksScope.Provider>
    </ChatBlocksHost.Provider>
  )
}

describe('choices schema', () => {
  const v = (o) => parseBlock('choices', json(o), { choices: validateChoices }).status
  it('validates shape and limits', () => {
    expect(v(spec)).toBe('ok')
    expect(v({ options: [{ id: 'a', label: 'x' }] })).toBe('invalid')
    expect(v({ options: [{ id: 'a', label: 'x' }, { id: 'a', label: 'y' }] })).toBe('invalid')
    expect(v({ options: [{ id: 'a b', label: 'x' }, { id: 'c', label: 'y' }] })).toBe('invalid')
    expect(v({ options: Array.from({ length: 13 }, (_, i) => ({ id: `i${i}`, label: 'x' })) })).toBe('invalid')
    expect(v({ ...spec, multi: 'yes' })).toBe('invalid')
  })
})

describe('ChoicesBlock', () => {
  it('sends the clicked option and then shows it answered', async () => {
    const onSend = vi.fn().mockResolvedValue(true)
    render(view(doc(spec), { onSend }))
    fireEvent.click(screen.getByRole('button', { name: /dev/ }))
    await waitFor(() => expect(onSend).toHaveBeenCalledWith('Which branch?: dev'))
    await waitFor(() => expect(screen.getByText('Answered')).toBeInTheDocument())
    expect(screen.getByRole('button', { name: /main/ })).toBeDisabled()
  })
  it('replays an answered block from storage', async () => {
    const onSend = vi.fn().mockResolvedValue(true)
    const { unmount } = render(view(doc(spec), { onSend }))
    fireEvent.click(screen.getByRole('button', { name: /main/ }))
    await waitFor(() => screen.getByText('Answered'))
    unmount()
    render(view(doc(spec), { onSend }))
    expect(screen.getByText('Answered')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /dev/ })).toBeDisabled()
  })
  it('stays answerable when the send fails', async () => {
    const onSend = vi.fn().mockResolvedValue(false)
    render(view(doc(spec), { onSend }))
    fireEvent.click(screen.getByRole('button', { name: /main/ }))
    await waitFor(() => expect(onSend).toHaveBeenCalled())
    expect(screen.queryByText('Answered')).toBeNull()
  })
  it('multi: sends the joined labels on confirm', async () => {
    const onSend = vi.fn().mockResolvedValue(true)
    render(view(doc({ ...spec, multi: true }), { onSend }))
    expect(screen.getByRole('button', { name: 'Confirm' })).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: /main/ }))
    fireEvent.click(screen.getByRole('button', { name: /dev/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Confirm' }))
    await waitFor(() => expect(onSend).toHaveBeenCalledWith('Which branch?: main, dev'))
  })
  it('renders disabled with a note when it cannot send', () => {
    render(view(doc(spec), {}))
    expect(screen.getByRole('button', { name: /main/ })).toBeDisabled()
    expect(screen.getByText(/not available/i)).toBeInTheDocument()
  })
  it('shows a skeleton for half-streamed JSON and a plain code block when invalid after the turn', () => {
    const partial = '```choices\n{"options":[{"id":"a","la'
    const { unmount } = render(view(partial, { live: true }))
    expect(screen.getByTestId('chat-block-skeleton')).toBeInTheDocument()
    unmount()
    render(view(doc('{"options": "nope"}')))
    expect(screen.queryByTestId('chat-block-choices')).toBeNull()
    expect(screen.getByText('{"options": "nope"}')).toBeInTheDocument()
  })
})
