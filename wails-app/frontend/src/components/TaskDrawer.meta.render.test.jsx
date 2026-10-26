// @vitest-environment jsdom
// The planner's closing [mission-meta] line: shown as chips, never as text in the notes box, and written back unchanged on save.
import { afterEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import '../i18n.js'

vi.mock('../services/tasks.js', () => ({ tasksApi: { show: vi.fn(async () => ({ events: [] })) } }))
import TaskDrawer from './TaskDrawer.jsx'
import { readFirst, matches } from '../lib/taskModel.js'

const LINE = '[mission-meta] {"files":["internal/a.go","b.go"],"size":"m","risk":"low"}'
const mount = (notes, extra = {}) => {
  const onEdit = vi.fn()
  render(<TaskDrawer task={{ id: 5, title: 'T', notes }} status="ready" onClose={() => {}} onEdit={onEdit} onComment={() => {}} {...extra} />)
  return onEdit
}
afterEach(cleanup)

describe('TaskDrawer with a mission-meta line', () => {
  it('shows only the text in the notes box, and files, size and risk as read-only chips', () => {
    mount(`Build the export.\n\n${LINE}`)
    expect(screen.getByLabelText('Notes')).toHaveValue('Build the export.')
    expect(document.body.textContent).not.toContain('[mission-meta]')
    const meta = screen.getByTestId('task-meta')
    for (const s of ['internal/a.go', 'b.go', 'm', 'low']) expect(meta).toHaveTextContent(s)
    expect(meta.querySelector('input, textarea, button')).toBeNull()
  })
  it('re-appends the line, once and unchanged, when edited notes are saved', () => {
    const onEdit = mount(`Build the export.\n\n${LINE}`)
    fireEvent.change(screen.getByLabelText('Notes'), { target: { value: 'Build the export, fast.' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(onEdit).toHaveBeenCalledWith(5, { notes: `Build the export, fast.\n\n${LINE}` })
  })
  it('is not dirty until the text changes, and a title-only save sends no notes', () => {
    const onEdit = mount(`x\n\n${LINE}`)
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
    fireEvent.change(screen.getByLabelText('Title'), { target: { value: 'T2' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(onEdit).toHaveBeenCalledWith(5, { title: 'T2' })
  })
  it('keeps the line when the text is emptied', () => {
    const onEdit = mount(`x\n\n${LINE}`)
    fireEvent.change(screen.getByLabelText('Notes'), { target: { value: '' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(onEdit).toHaveBeenCalledWith(5, { notes: LINE })
  })
  it('drops an empty meta object from the display but still writes it back', () => {
    const onEdit = mount('Epic.\n\n[mission-meta] {}')
    expect(screen.queryByTestId('task-meta')).not.toBeInTheDocument()
    expect(document.body.textContent).not.toContain('[mission-meta]')
    fireEvent.change(screen.getByLabelText('Notes'), { target: { value: 'Epic 2.' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(onEdit).toHaveBeenCalledWith(5, { notes: 'Epic 2.\n\n[mission-meta] {}' })
  })
  it('shows a malformed line as plain text in the notes', () => {
    mount('x\n\n[mission-meta] {"files":')
    expect(screen.getByLabelText('Notes')).toHaveValue('x\n\n[mission-meta] {"files":')
    expect(screen.queryByTestId('task-meta')).not.toBeInTheDocument()
  })
})

describe('cards and search', () => {
  it('a card whose notes are only the meta line is not read-first, and search does not match the line', () => {
    expect(readFirst({ notes: '[mission-meta] {}' })).toBe(false)
    expect(readFirst({ notes: `real\n\n${LINE}` })).toBe(true)
    expect(matches({ title: 'a', notes: `real\n\n${LINE}` }, 'mission-meta')).toBe(false)
    expect(matches({ title: 'a', notes: `real\n\n${LINE}` }, 'real')).toBe(true)
  })
})
