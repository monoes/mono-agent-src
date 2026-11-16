// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, cleanup, fireEvent, within } from '@testing-library/react'
import '../../../i18n.js'

vi.mock('../../../services/api.js', () => ({ api: { openURL: vi.fn(), classifyChatPath: vi.fn(), resolveChatPath: vi.fn() }, notify: vi.fn() }))
import { ChatMarkdown } from '../ChatMarkdown.jsx'
import { ChatBlocksHost } from './context.js'
import { validateTable, sortRows } from './tableSchema.js'
import { parseBlock } from './parse.js'

afterEach(cleanup)
const md = (o) => `\`\`\`table\n${JSON.stringify(o)}\n\`\`\``
const spec = {
  title: 'Workflows',
  columns: [{ key: 'name', label: 'Name' }, { key: 'runs', label: 'Runs', align: 'right' }],
  rows: [{ name: 'b', runs: 10, link: 'monoagent://workflow/w2' }, { name: 'a', runs: 9 }, { name: 'c', runs: null }],
}
const names = () => screen.getAllByRole('row').slice(1).map(r => within(r).getAllByRole('cell')[0].textContent)

describe('table schema', () => {
  const st = (o) => parseBlock('table', JSON.stringify(o), { table: validateTable }).status
  it('validates', () => {
    expect(st(spec)).toBe('ok')
    expect(st({ ...spec, rows: [] })).toBe('invalid')
    expect(st({ ...spec, rows: Array.from({ length: 101 }, () => ({ name: 'x' })) })).toBe('invalid')
    expect(st({ ...spec, columns: [{ key: 'link', label: 'L' }] })).toBe('invalid')
    expect(st({ ...spec, columns: [{ key: 'a', label: 'A' }, { key: 'a', label: 'B' }] })).toBe('invalid')
    expect(st({ ...spec, rows: [{ name: { x: 1 } }] })).toBe('invalid')
    expect(st({ ...spec, rows: [{ name: 'x', link: 'https://evil.example' }] })).toBe('invalid')
    expect(st({ ...spec, columns: Array.from({ length: 9 }, (_, i) => ({ key: `k${i}`, label: 'x' })) })).toBe('invalid')
  })
  it('sorts numbers numerically with empties last in both directions', () => {
    const rows = validateTable(spec).rows
    expect(sortRows(rows, 1, 'asc').map(r => r.cells[1])).toEqual([9, 10, null])
    expect(sortRows(rows, 1, 'desc').map(r => r.cells[1])).toEqual([10, 9, null])
  })
})

describe('TableBlock', () => {
  it('sorts on header click: asc, desc, then original order', () => {
    render(<ChatMarkdown content={md(spec)} />)
    expect(names()).toEqual(['b', 'a', 'c'])
    const h = screen.getByRole('button', { name: 'Name' })
    fireEvent.click(h)
    expect(names()).toEqual(['a', 'b', 'c'])
    fireEvent.click(h)
    expect(names()).toEqual(['c', 'b', 'a'])
    fireEvent.click(h)
    expect(names()).toEqual(['b', 'a', 'c'])
  })
  it('renders the row link as a button that calls onAppLink, and is a no-op without it', () => {
    const onAppLink = vi.fn()
    const { unmount } = render(<ChatBlocksHost.Provider value={{ onAppLink }}><ChatMarkdown content={md(spec)} /></ChatBlocksHost.Provider>)
    fireEvent.click(screen.getByRole('button', { name: /monoagent:\/\/workflow\/w2/ }))
    expect(onAppLink).toHaveBeenCalledWith('monoagent://workflow/w2')
    unmount()
    render(<ChatMarkdown content={md(spec)} />)
    expect(() => fireEvent.click(screen.getByRole('button', { name: /monoagent:\/\/workflow\/w2/ }))).not.toThrow()
    expect(document.querySelector('a')).toBeNull()
  })
})
