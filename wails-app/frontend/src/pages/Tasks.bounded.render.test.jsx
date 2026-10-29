// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import '../i18n.js'

const { api } = vi.hoisted(() => ({
  api: { board: vi.fn(), show: vi.fn(), add: vi.fn(), edit: vi.fn(), move: vi.fn(), approve: vi.fn(), archive: vi.fn(), comment: vi.fn(), agentShell: vi.fn() },
}))
vi.mock('../services/tasks.js', () => ({ tasksApi: api, onTasksChanged: () => () => {} }))
vi.mock('../services/taskSessions.js', () => ({ sessionsApi: { list: async () => ({ sessions: [] }) } }))
import Tasks from './Tasks.jsx'

const card = (id, extra = {}) => ({ id, title: `Task ${id}`, notes: '', position: id, claim: null, ...extra })
const many = (from, n) => Array.from({ length: n }, (_, i) => card(from + i))

beforeEach(() => {
  Object.values(api).forEach(f => f.mockReset())
  api.agentShell.mockResolvedValue('')
  api.board.mockResolvedValue({ rev: 1, counts: { inbox: 1000, ready: 3 }, tasks: { inbox: many(1, 1000), ready: many(5000, 3) } })
})
afterEach(cleanup)

const cardsIn = (region) => region.querySelectorAll('[data-task-id]')

describe('a long board renders a bounded number of cards', () => {
  it('shows the first 50 of a 1000-card column, the full count, and a control for the rest', async () => {
    render(<Tasks />)
    const inbox = await screen.findByRole('region', { name: 'Inbox' })
    await within(inbox).findByText('Task 1')
    expect(cardsIn(inbox).length).toBe(50)
    expect(within(inbox).getByText('1000')).toBeInTheDocument() // the column's count is the real one
    expect(within(inbox).getByRole('button', { name: /Show 50 more.*Inbox/ })).toBeInTheDocument()
    // a short column shows everything and no control
    const ready = screen.getByRole('region', { name: 'Ready' })
    expect(cardsIn(ready).length).toBe(3)
    expect(within(ready).queryByRole('button', { name: /Show .* more/ })).not.toBeInTheDocument()
  })

  it('shows more on request, 50 at a time, until the column is whole', async () => {
    render(<Tasks />)
    const inbox = await screen.findByRole('region', { name: 'Inbox' })
    await within(inbox).findByText('Task 1')
    fireEvent.click(within(inbox).getByRole('button', { name: /Show 50 more/ }))
    expect(cardsIn(inbox).length).toBe(100)
    for (let i = 0; i < 18; i++) fireEvent.click(within(inbox).getByRole('button', { name: /Show .* more/ }))
    expect(cardsIn(inbox).length).toBe(1000)
    expect(within(inbox).queryByRole('button', { name: /Show .* more/ })).not.toBeInTheDocument()
    // 19 clicks, each re-rendering up to 1000 cards in jsdom: about 13 s on an idle machine, so the global 30 s
    // is too tight when other work shares the CPU. The test asserts the same things; only its budget is larger.
  }, 120_000)

  it('searches the whole column, not only the cards on screen', async () => {
    render(<Tasks />)
    await screen.findByText('Task 1')
    fireEvent.change(screen.getByLabelText('Search tasks'), { target: { value: 'Task 987' } })
    const inbox = screen.getByRole('region', { name: 'Inbox' })
    expect(within(inbox).getByText('Task 987')).toBeInTheDocument()
    expect(cardsIn(inbox).length).toBe(1)
  })

  it('keeps arrow-key movement inside the cards that are on screen', async () => {
    render(<Tasks />)
    const inbox = await screen.findByRole('region', { name: 'Inbox' })
    const last = await within(inbox).findByRole('button', { name: 'Task 50' })
    last.focus()
    fireEvent.keyDown(last, { key: 'ArrowDown' })
    expect(last).toHaveFocus() // there is no card 51 on screen to land on, and nothing breaks
    expect(cardsIn(inbox).length).toBe(50)
  })
})
