// @vitest-environment jsdom
// States found in the browser pass: the empty columns of a session board, and a board that fails to read.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import '../../i18n.js'

const { api, sess } = vi.hoisted(() => ({
  api: { board: vi.fn(), show: vi.fn(), add: vi.fn(), edit: vi.fn(), move: vi.fn(), approve: vi.fn(), archive: vi.fn(), comment: vi.fn(), agentShell: vi.fn() },
  sess: { list: vi.fn(), show: vi.fn(), plan: vi.fn(), board: vi.fn(), approve: vi.fn(), pause: vi.fn(), resume: vi.fn(), stop: vi.fn(), archive: vi.fn(), budget: vi.fn(), pulse: vi.fn() },
}))
vi.mock('../../services/tasks.js', () => ({ tasksApi: api, onTasksChanged: () => () => {} }))
vi.mock('../../services/taskSessions.js', () => ({ sessionsApi: sess }))
import Tasks from '../../pages/Tasks.jsx'

const mission = { id: 1, title: 'Mission 1', goal: 'g', state: 'done', archived: false, rev: 1, budget_usd: 0, spent_usd: 0, counts: { done: 0, total: 0 } }

beforeEach(() => {
  Object.values(api).forEach(f => f.mockReset())
  Object.values(sess).forEach(f => f.mockReset())
  api.board.mockResolvedValue({ rev: 1, counts: {}, tasks: {} })
  api.agentShell.mockResolvedValue('')
  sess.list.mockResolvedValue({ sessions: [mission] })
  sess.show.mockResolvedValue({ session: mission, events: [] })
  sess.pulse.mockResolvedValue({ session_id: 1, rev: 1, state: 'done' })
})
afterEach(cleanup)

describe('session board states', () => {
  it('does not tell a session to highlight text in Chrome or approve from Inbox', async () => {
    sess.board.mockResolvedValue({ rev: 1, counts: {}, tasks: {} })
    render(<Tasks />)
    fireEvent.click(await screen.findByRole('tab', { name: /Mission 1/ }))
    expect(await screen.findByRole('region', { name: 'Session Mission 1' })).toBeInTheDocument()
    await screen.findByLabelText('Inbox')
    expect(screen.queryByText(/Highlight text in Chrome/)).not.toBeInTheDocument()
    expect(screen.queryByText(/Approve tasks from Inbox/)).not.toBeInTheDocument()
  })

  it('shows the read error, and not Loading the board, when a session board cannot be read', async () => {
    sess.board.mockResolvedValue({ error: 'cli failed' })
    render(<Tasks />)
    fireEvent.click(await screen.findByRole('tab', { name: /Mission 1/ }))
    expect(await screen.findByText(/cli failed/)).toBeInTheDocument()
    expect(screen.queryByText('Loading the board…')).not.toBeInTheDocument()
  })
})
