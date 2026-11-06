// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, cleanup, waitFor } from '@testing-library/react'
import '../../i18n.js'

const { list } = vi.hoisted(() => ({ list: vi.fn() }))
vi.mock('../../services/taskSessions.js', () => ({ sessionsApi: { list } }))
import OpenRunBoss from './OpenRunBoss.jsx'
import { CoderBubbleContext } from '../bubbles/CoderBubbleContext.jsx'

afterEach(() => { cleanup(); list.mockReset() })

const row = (id, extra = {}) => ({ id, parent_session_id: 0, conversation_id: `c${id}`, title: 'Boss', state: 'running', counts: {}, spent_usd: 1.5, ...extra })

describe('the open-ended run on the Agents page', () => {
  it('lists the recorded boss with its state and opens its board', async () => {
    list.mockResolvedValue({ sessions: [row(3, { boss: true, client_id: 'boss-for-x' }), row(4)] })
    const navigate = vi.fn()
    render(<CoderBubbleContext.Provider value={{ navigate }}><OpenRunBoss /></CoderBubbleContext.Provider>)
    const section = await screen.findByTestId('open-run-boss')
    expect(section).toHaveTextContent('Boss #3')
    expect(section).toHaveTextContent('State: Running')
    expect(section).toHaveTextContent('Spent: $1.50')
    fireEvent.click(screen.getByRole('button', { name: 'Open its board' }))
    expect(navigate).toHaveBeenCalledWith('tasks', { sessionId: 3 })
  })
  it('renders nothing while no recorded boss is open', async () => {
    list.mockResolvedValue({ sessions: [row(4), row(5, { boss: true, state: 'done' })] })
    const { container } = render(<OpenRunBoss />)
    await waitFor(() => expect(list).toHaveBeenCalled())
    expect(container).toBeEmptyDOMElement()
  })
})
