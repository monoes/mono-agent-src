// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import '../../i18n.js'
import SessionSwitcher from './SessionSwitcher.jsx'

const mk = (id, extra = {}) => ({ id, title: `Mission ${id}`, state: 'running', counts: { done: 1, total: 2 }, budget_usd: 0, spent_usd: 0, ...extra })
afterEach(cleanup)

const setup = (props = {}) => {
  const p = { sessions: [], selectedId: null, onSelect: vi.fn(), onCreate: vi.fn(async () => ({ session: { id: 9 } })), onRename: vi.fn(async () => ({ session: { id: 1 } })), ...props }
  return { p, ...render(<SessionSwitcher {...p} />) }
}

describe('SessionSwitcher with no sessions', () => {
  it('still shows Personal and New session', () => {
    setup()
    expect(screen.getByRole('tab', { name: 'Personal' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'New session' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Rename' })).toBeNull()
  })
})

describe('create flow', () => {
  it('needs a title, sends title/goal/parent, then selects the new session', async () => {
    const { p } = setup({ sessions: [mk(1), mk(2)] })
    fireEvent.click(screen.getByRole('button', { name: 'New session' }))
    fireEvent.click(screen.getByRole('button', { name: 'Create session' }))
    expect(screen.getByRole('alert')).toHaveTextContent('Give the session a title.')
    expect(p.onCreate).not.toHaveBeenCalled()
    fireEvent.change(screen.getByLabelText('Title'), { target: { value: '  Plan <b>x</b> ' } })
    fireEvent.change(screen.getByLabelText('Goal (optional)'), { target: { value: 'Ship it' } })
    fireEvent.change(screen.getByLabelText('Parent session'), { target: { value: '2' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create session' }))
    await waitFor(() => expect(p.onSelect).toHaveBeenCalledWith(9))
    expect(p.onCreate).toHaveBeenCalledWith({ title: 'Plan <b>x</b>', goal: 'Ship it', parentId: 2 })
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('refuses a too long title without cutting it, and shows a CLI refusal with a retry', async () => {
    const onCreate = vi.fn().mockResolvedValueOnce({ error: 'depth limit' }).mockResolvedValueOnce({ session: { id: 5 } })
    const { p } = setup({ onCreate })
    fireEvent.click(screen.getByRole('button', { name: 'New session' }))
    fireEvent.change(screen.getByLabelText('Title'), { target: { value: 'x'.repeat(201) } })
    fireEvent.click(screen.getByRole('button', { name: 'Create session' }))
    expect(screen.getByRole('alert')).toHaveTextContent('longer than 200')
    expect(onCreate).not.toHaveBeenCalled()
    fireEvent.change(screen.getByLabelText('Title'), { target: { value: 'ok' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create session' }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('depth limit'))
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(p.onSelect).toHaveBeenCalledWith(5))
    expect(onCreate).toHaveBeenCalledTimes(2)
  })

  it('Escape closes the form', () => {
    setup()
    fireEvent.click(screen.getByRole('button', { name: 'New session' }))
    fireEvent.keyDown(screen.getByLabelText('Title'), { key: 'Escape' })
    expect(screen.queryByRole('dialog')).toBeNull()
  })
})

describe('rename flow', () => {
  it('Enter saves the trimmed title, Escape cancels', async () => {
    const { p } = setup({ sessions: [mk(1)], selectedId: 1 })
    fireEvent.click(screen.getByRole('button', { name: 'Rename' }))
    const input = screen.getByLabelText('Session title')
    expect(input).toHaveValue('Mission 1')
    fireEvent.keyDown(input, { key: 'Escape' })
    expect(screen.queryByLabelText('Session title')).toBeNull()
    expect(p.onRename).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Rename' }))
    fireEvent.change(screen.getByLabelText('Session title'), { target: { value: ' Better ' } })
    fireEvent.keyDown(screen.getByLabelText('Session title'), { key: 'Enter' })
    await waitFor(() => expect(p.onRename).toHaveBeenCalledWith(1, 'Better'))
    await waitFor(() => expect(screen.queryByLabelText('Session title')).toBeNull())
  })

  it('tells the user a title is refused and keeps the field; a CLI error offers a retry', async () => {
    const onRename = vi.fn().mockResolvedValueOnce({ error: 'not yours' }).mockResolvedValueOnce({})
    setup({ sessions: [mk(1)], selectedId: 1, onRename })
    fireEvent.click(screen.getByRole('button', { name: 'Rename' }))
    const input = () => screen.getByLabelText('Session title')
    fireEvent.change(input(), { target: { value: 'y'.repeat(201) } })
    fireEvent.keyDown(input(), { key: 'Enter' })
    expect(screen.getByRole('alert')).toHaveTextContent('longer than 200')
    expect(input()).toHaveValue('y'.repeat(201))
    fireEvent.change(input(), { target: { value: 'fine' } })
    fireEvent.keyDown(input(), { key: 'Enter' })
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('not yours'))
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(screen.queryByLabelText('Session title')).toBeNull())
    expect(onRename).toHaveBeenCalledTimes(2)
  })
})

describe('flat tabs keep roving focus', () => {
  it('ArrowRight selects the next tab, wrapping past the last', () => {
    const { p } = setup({ sessions: [mk(1), mk(2)], selectedId: 2 })
    fireEvent.keyDown(screen.getByRole('tab', { name: /Mission 2/ }), { key: 'ArrowRight' })
    expect(p.onSelect).toHaveBeenCalledWith(null)
  })
})

describe('tree', () => {
  const sessions = [mk(1), mk(2, { parent_session_id: 1, created_by: 'lead' }), mk(3, { parent_session_id: 2 }), mk(4)]
  it('renders a tree with levels, creator, progress and spend', () => {
    setup({ sessions, selectedId: 1 })
    expect(screen.getByRole('tree')).toBeInTheDocument()
    expect(screen.queryByRole('tablist')).toBeNull()
    const levels = screen.getAllByRole('treeitem').map(r => r.getAttribute('aria-level'))
    expect(levels).toEqual(['1', '1', '2', '3', '1'])
    expect(screen.getByTestId('ts-creator')).toHaveTextContent('by lead')
    expect(screen.getAllByTestId('ts-progress')).toHaveLength(4)
    expect(screen.getAllByTestId('ts-spend')).toHaveLength(4)
  })

  it('Up/Down move focus, Left collapses, Right expands, Enter selects', async () => {
    const { p } = setup({ sessions, selectedId: 1 })
    const item = (n) => screen.getByRole('treeitem', { name: new RegExp(n) })
    const root = item('Mission 1')
    root.focus()
    fireEvent.keyDown(root, { key: 'ArrowLeft' })
    expect(root).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryByRole('treeitem', { name: /Mission 2/ })).toBeNull()
    fireEvent.keyDown(root, { key: 'ArrowRight' })
    expect(root).toHaveAttribute('aria-expanded', 'true')
    fireEvent.keyDown(root, { key: 'ArrowRight' })
    await waitFor(() => expect(item('Mission 2')).toHaveFocus())
    fireEvent.keyDown(item('Mission 2'), { key: 'ArrowDown' })
    await waitFor(() => expect(item('Mission 3')).toHaveFocus())
    fireEvent.keyDown(item('Mission 3'), { key: 'ArrowLeft' }) // a leaf goes to its parent
    await waitFor(() => expect(item('Mission 2')).toHaveFocus())
    fireEvent.keyDown(item('Mission 2'), { key: 'ArrowUp' })
    await waitFor(() => expect(root).toHaveFocus())
    fireEvent.keyDown(item('Mission 2'), { key: 'Enter' })
    expect(p.onSelect).toHaveBeenCalledWith(2)
  })

  it('renders titles as text, never as HTML', () => {
    setup({ sessions: [mk(1, { title: '<img src=x onerror=alert(1)>' }), mk(2, { parent_session_id: 1 })] })
    expect(document.querySelector('img')).toBeNull()
    expect(screen.getByText('<img src=x onerror=alert(1)>')).toBeInTheDocument()
  })
})

describe('list error', () => {
  it('shows the inline error with a retry beside Personal', () => {
    const onRetry = vi.fn()
    setup({ error: true, onRetry })
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(onRetry).toHaveBeenCalled()
    expect(screen.getByRole('tab', { name: 'Personal' })).toBeInTheDocument()
  })
})

describe('a long list of sessions renders a bounded number of tabs and rows', () => {
  const many = (n, extra = () => ({})) => Array.from({ length: n }, (_, i) => mk(i + 1, extra(i + 1)))

  it('draws the first 100 tabs of 1000, then 100 more on request', () => {
    setup({ sessions: many(1000) })
    expect(screen.getAllByRole('tab')).toHaveLength(101) // Personal + 100
    fireEvent.click(screen.getByRole('button', { name: /Show 100 more sessions \(900 left\)/ }))
    expect(screen.getAllByRole('tab')).toHaveLength(201)
  })

  it('keeps the chosen session on screen even when it is past the first page', () => {
    setup({ sessions: many(1000), selectedId: 500 })
    expect(screen.getAllByRole('tab')).toHaveLength(501)
    expect(screen.getByRole('tab', { name: /Mission 500/ })).toHaveAttribute('aria-selected', 'true')
  })

  it('a list within the page shows every tab and no control', () => {
    setup({ sessions: many(100) })
    expect(screen.getAllByRole('tab')).toHaveLength(101)
    expect(screen.queryByRole('button', { name: /more sessions/ })).toBeNull()
  })

  it('bounds the tree the same way', () => {
    setup({ sessions: many(1000, (id) => (id > 1 ? { parent_session_id: 1 } : {})) })
    expect(screen.getAllByRole('treeitem')).toHaveLength(101)
    fireEvent.click(screen.getByRole('button', { name: /Show 100 more sessions/ }))
    expect(screen.getAllByRole('treeitem')).toHaveLength(201)
  })
})
