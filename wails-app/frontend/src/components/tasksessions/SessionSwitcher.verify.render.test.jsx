// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import '../../i18n.js'
import SessionSwitcher from './SessionSwitcher.jsx'
import SessionTree from './SessionTree.jsx'

const mk = (id, extra = {}) => ({ id, title: `Mission ${id}`, state: 'running', counts: { done: 1, total: 2 }, budget_usd: 0, spent_usd: 0, ...extra })
afterEach(cleanup)
const nested = [mk(1), mk(2, { parent_session_id: 1 }), mk(3, { parent_session_id: 2 })]

describe('fields use the app form classes (a bare "input" class is unstyled: white boxes in the dark theme)', () => {
  it('New session and Rename', () => {
    const { unmount } = render(<SessionSwitcher sessions={[mk(1)]} selectedId={1} onSelect={vi.fn()} onCreate={vi.fn()} onRename={vi.fn()} />)
    fireEvent.click(screen.getByRole('button', { name: 'New session' }))
    expect(screen.getByLabelText('Title')).toHaveClass('form-input')
    expect(screen.getByLabelText('Goal (optional)')).toHaveClass('form-textarea')
    expect(screen.getByLabelText('Parent session')).toHaveClass('form-select')
    unmount()
    render(<SessionSwitcher sessions={[mk(1)]} selectedId={1} onSelect={vi.fn()} onCreate={vi.fn()} onRename={vi.fn()} />)
    fireEvent.click(screen.getByRole('button', { name: 'Rename' }))
    expect(screen.getByLabelText('Session title')).toHaveClass('form-input')
  })
})

describe('Retry is for a refusal of the CLI, not for our own title check', () => {
  it('create', async () => {
    const onCreate = vi.fn().mockResolvedValue({ error: 'depth limit' })
    render(<SessionSwitcher sessions={[]} selectedId={null} onSelect={vi.fn()} onCreate={onCreate} />)
    fireEvent.click(screen.getByRole('button', { name: 'New session' }))
    fireEvent.click(screen.getByRole('button', { name: 'Create session' }))
    expect(screen.getByRole('alert')).toHaveTextContent('Give the session a title.')
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull()
    fireEvent.change(screen.getByLabelText('Title'), { target: { value: 'ok' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create session' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Retry' })).toBeInTheDocument())
  })
  it('rename', async () => {
    const onRename = vi.fn().mockResolvedValue({ error: 'not yours' })
    render(<SessionSwitcher sessions={[mk(1)]} selectedId={1} onSelect={vi.fn()} onRename={onRename} />)
    fireEvent.click(screen.getByRole('button', { name: 'Rename' }))
    fireEvent.change(screen.getByLabelText('Session title'), { target: { value: 'x'.repeat(201) } })
    fireEvent.keyDown(screen.getByLabelText('Session title'), { key: 'Enter' })
    expect(screen.getByRole('alert')).toHaveTextContent('longer than 200')
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull()
    fireEvent.change(screen.getByLabelText('Session title'), { target: { value: 'fine' } })
    fireEvent.keyDown(screen.getByLabelText('Session title'), { key: 'Enter' })
    await waitFor(() => expect(screen.getByRole('button', { name: 'Retry' })).toBeInTheDocument())
  })
})

describe('focus goes back to the button of a form that closes', () => {
  it('Escape in New session and in Rename', async () => {
    render(<SessionSwitcher sessions={[mk(1)]} selectedId={1} onSelect={vi.fn()} onCreate={vi.fn()} onRename={vi.fn()} />)
    fireEvent.click(screen.getByRole('button', { name: 'New session' }))
    fireEvent.keyDown(screen.getByLabelText('Title'), { key: 'Escape' })
    await waitFor(() => expect(screen.getByRole('button', { name: 'New session' })).toHaveFocus())
    fireEvent.click(screen.getByRole('button', { name: 'Rename' }))
    fireEvent.keyDown(screen.getByLabelText('Session title'), { key: 'Escape' })
    await waitFor(() => expect(screen.getByRole('button', { name: 'Rename' })).toHaveFocus())
  })
})

describe('the tree survives the remount a choice causes (the page is keyed by its session)', () => {
  const ui = () => ({ current: { collapsed: new Set(), refocus: false } })
  it('keeps the collapsed rows', () => {
    const shared = ui()
    const first = render(<SessionTree sessions={nested} selectedId={null} onSelect={vi.fn()} ui={shared} />)
    fireEvent.keyDown(screen.getByRole('treeitem', { name: /Mission 2/ }), { key: 'ArrowLeft' })
    expect(screen.queryByRole('treeitem', { name: /Mission 3/ })).toBeNull()
    first.unmount()
    render(<SessionTree sessions={nested} selectedId={2} onSelect={vi.fn()} ui={shared} />)
    expect(screen.getByRole('treeitem', { name: /Mission 2/ })).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryByRole('treeitem', { name: /Mission 3/ })).toBeNull()
  })
  it('puts the focus back on the chosen row after Enter', () => {
    const shared = ui()
    const onSelect = vi.fn()
    const first = render(<SessionTree sessions={nested} selectedId={null} onSelect={onSelect} ui={shared} />)
    fireEvent.keyDown(screen.getByRole('treeitem', { name: /Mission 2/ }), { key: 'Enter' })
    expect(onSelect).toHaveBeenCalledWith(2)
    first.unmount()
    render(<SessionTree sessions={nested} selectedId={2} onSelect={onSelect} ui={shared} />)
    expect(screen.getByRole('treeitem', { name: /Mission 2/ })).toHaveFocus()
    expect(shared.current.refocus).toBe(false)
  })
  it('does not steal the focus when the choice was a click', () => {
    const shared = ui()
    render(<SessionTree sessions={nested} selectedId={2} onSelect={vi.fn()} ui={shared} />)
    expect(document.body).toHaveFocus()
  })
})
