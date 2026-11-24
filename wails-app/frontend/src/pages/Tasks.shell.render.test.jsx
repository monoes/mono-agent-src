// @vitest-environment jsdom
// W4 r21-D-05: a failed agentShell() check keeps the board locked and says why.
import { afterEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { cleanup, render, screen } from '@testing-library/react'
import '../i18n.js'

const { api } = vi.hoisted(() => ({
  api: { board: vi.fn(), show: vi.fn(), add: vi.fn(), edit: vi.fn(), move: vi.fn(), approve: vi.fn(), archive: vi.fn(), comment: vi.fn(), agentShell: vi.fn() },
}))
vi.mock('../services/tasks.js', () => ({ tasksApi: api, onTasksChanged: () => () => {} }))
vi.mock('../services/taskSessions.js', () => ({ sessionsApi: { list: async () => ({ sessions: [] }) } }))
import Tasks from './Tasks.jsx'

afterEach(() => { cleanup(); Object.values(api).forEach(f => f.mockReset()) })

describe('Tasks agent-shell check', () => {
  it('fails closed with a visible reason', async () => {
    api.board.mockResolvedValue({ rev: 1, counts: {}, tasks: { inbox: [], ready: [] } })
    api.agentShell.mockRejectedValue(new Error('binding gone'))
    render(<Tasks />)
    expect(await screen.findByTestId('tb-shell-error')).toHaveTextContent('binding gone')
    expect(screen.queryByRole('button', { name: /New task/ })).toBeNull()
  })
})
