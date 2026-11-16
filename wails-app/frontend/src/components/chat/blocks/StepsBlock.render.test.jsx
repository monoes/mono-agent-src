// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup } from '@testing-library/react'
import '../../../i18n.js'

vi.mock('../../../services/api.js', () => ({ api: { openURL: vi.fn(), classifyChatPath: vi.fn(), resolveChatPath: vi.fn() }, notify: vi.fn() }))
import { ChatMarkdown } from '../ChatMarkdown.jsx'
import { validateSteps } from './StepsBlock.jsx'
import { parseBlock } from './parse.js'

afterEach(cleanup)
const fence = (o) => `\`\`\`steps\n${JSON.stringify(o)}\n\`\`\``
const spec = { title: 'Deploy', steps: [{ label: 'Build', state: 'done' }, { label: 'Test', state: 'active', detail: '3 of 9' }, { label: 'Ship', state: 'todo' }] }

describe('steps', () => {
  it('validates', () => {
    const st = (o) => parseBlock('steps', JSON.stringify(o), { steps: validateSteps }).status
    expect(st(spec)).toBe('ok')
    expect(st({ steps: [] })).toBe('invalid')
    expect(st({ steps: [{ label: 'x', state: 'paused' }] })).toBe('invalid')
    expect(st({ steps: Array.from({ length: 31 }, () => ({ label: 'x', state: 'todo' })) })).toBe('invalid')
  })
  it('renders the list with progress and state text', () => {
    render(<ChatMarkdown content={fence(spec)} />)
    expect(screen.getAllByRole('listitem')).toHaveLength(3)
    const bar = screen.getByRole('progressbar')
    expect(bar).toHaveAttribute('aria-valuenow', '1')
    expect(bar).toHaveAttribute('aria-valuemax', '3')
    expect(screen.getByText('(in progress)')).toBeInTheDocument()
    expect(screen.getByText('3 of 9')).toBeInTheDocument()
  })
})
