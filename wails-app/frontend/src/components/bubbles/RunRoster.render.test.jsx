// @vitest-environment jsdom
import React from 'react'
import { describe, it, expect, vi, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, cleanup, within } from '@testing-library/react'
import '../../i18n.js'
import { RunRoster } from './RunRoster.jsx'
import { runRoster } from '../../lib/sessionOrbit.js'

afterEach(cleanup)

const S = (id, parent, extra = {}) => ({ id, parent_session_id: parent, conversation_id: `c${id}`, title: `Agent ${id}`, state: 'running', counts: {}, ...extra })
const list = [
  S(1, 0), S(9, 0, { title: 'Boss', boss: true, client_id: 'boss-for-c1-1' }),
  S(20, 9, { kind: 'direction', title: 'Plan it' }), S(30, 20, { kind: 'task', title: 'Do it', state: 'waiting' }),
  S(31, 20, { kind: 'task', title: 'Board only', conversation_id: '' }),
]

describe('the run roster', () => {
  it('shows the front desk, the boss, the planners and the executors, each a labelled card', () => {
    render(<RunRoster roster={runRoster(list, 'c1')} activeId={null} onSelect={() => {}} deskTitle="Desk chat" />)
    for (const g of ['Front desk', 'Boss', 'Planners', 'Executors']) expect(screen.getByRole('group', { name: g })).toBeInTheDocument()
    expect(within(screen.getByRole('group', { name: 'Planners' })).getByText('Plan it')).toBeInTheDocument()
    expect(screen.getByTestId('roster-desk')).toHaveAttribute('aria-pressed', 'true')
  })
  it('selects a role by clicking its card, and the front desk with null', () => {
    const onSelect = vi.fn()
    render(<RunRoster roster={runRoster(list, 'c1')} activeId={9} onSelect={onSelect} deskTitle="Desk chat" />)
    expect(screen.getByTestId('roster-role-9')).toHaveAttribute('aria-pressed', 'true')
    fireEvent.click(screen.getByTestId('roster-role-30'))
    expect(onSelect).toHaveBeenCalledWith(expect.objectContaining({ id: 30, conversationId: 'c30' }))
    fireEvent.click(screen.getByTestId('roster-desk'))
    expect(onSelect).toHaveBeenLastCalledWith(null)
  })
})
