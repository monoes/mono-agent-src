// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'
import '../../../i18n.js'

vi.mock('../../../services/api.js', () => ({ api: { openURL: vi.fn(), classifyChatPath: vi.fn(), resolveChatPath: vi.fn() }, notify: vi.fn() }))
import { ChatMarkdown } from '../ChatMarkdown.jsx'
import { ChatBlocksHost } from './context.js'
import { validateCard, validateList } from './cardSchema.js'
import { parseBlock } from './parse.js'

afterEach(cleanup)
const fence = (t, o) => `\`\`\`${t}\n${JSON.stringify(o)}\n\`\`\``
const card = { kind: 'workflow', title: 'Nightly sync', subtitle: 'every day 02:00', status: 'failed', tone: 'error', link: 'monoagent://workflow/w1' }

describe('card / list schema', () => {
  it('validates entities and lists', () => {
    expect(parseBlock('card', JSON.stringify(card), { card: validateCard }).status).toBe('ok')
    expect(parseBlock('card', JSON.stringify({ kind: 'x' }), { card: validateCard }).status).toBe('invalid')
    expect(parseBlock('card', JSON.stringify({ ...card, tone: 'purple' }), { card: validateCard }).status).toBe('invalid')
    expect(parseBlock('card', JSON.stringify({ ...card, link: 'javascript:alert(1)' }), { card: validateCard }).status).toBe('invalid')
    const l = (items) => parseBlock('list', JSON.stringify({ items }), { list: validateList }).status
    expect(l([card])).toBe('ok')
    expect(l([])).toBe('invalid')
    expect(l(Array.from({ length: 25 }, () => card))).toBe('invalid')
  })
})

describe('card / list blocks', () => {
  it('renders a card and opens its link through onAppLink', () => {
    const onAppLink = vi.fn()
    render(<ChatBlocksHost.Provider value={{ onAppLink }}><ChatMarkdown content={fence('card', card)} /></ChatBlocksHost.Provider>)
    expect(screen.getByText('Nightly sync')).toBeInTheDocument()
    expect(screen.getByText('failed')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /monoagent:\/\/workflow\/w1/ }))
    expect(onAppLink).toHaveBeenCalledWith('monoagent://workflow/w1')
  })
  it('renders a list of entities; an item without a link has no button', () => {
    render(<ChatMarkdown content={fence('list', { title: 'Failing', items: [card, { kind: 'person', title: 'Ada' }] })} />)
    expect(screen.getAllByRole('listitem')).toHaveLength(2)
    expect(screen.getAllByRole('button')).toHaveLength(1)
  })
})
