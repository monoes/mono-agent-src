// @vitest-environment jsdom
import { afterEach, describe, expect, it } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { cleanup, render, screen } from '@testing-library/react'
import '../../i18n.js'
import SessionSwitcher from './SessionSwitcher.jsx'
import SessionHeader from './SessionHeader.jsx'
import BossBadge from './BossBadge.jsx'

afterEach(cleanup)
const S = (id, extra = {}) => ({ id, title: `Mission ${id}`, state: 'running', counts: { done: 0, total: 1 }, budget_usd: 0, spent_usd: 0, parent_session_id: 0, ...extra })

describe('the boss mark on session lists', () => {
  it('is on the boss tab of the flat strip and on no other', () => {
    render(<SessionSwitcher sessions={[S(1), S(2, { boss: true })]} selectedId={null} onSelect={() => {}} />)
    expect(screen.getAllByTestId('ts-boss')).toHaveLength(1)
    expect(screen.getByTestId('ts-boss').closest('[data-ts-tab]')).toHaveTextContent('Mission 2')
  })
  it('is on the boss row of the tree', () => {
    render(<SessionSwitcher sessions={[S(1), S(2, { boss: true }), S(3, { parent_session_id: 1 })]} selectedId={null} onSelect={() => {}} />)
    expect(screen.getAllByTestId('ts-boss')).toHaveLength(1)
    expect(screen.getByTestId('ts-boss').closest('[data-ts-row]')).toHaveTextContent('Mission 2')
  })
  it('is in the header of the chosen boss session', () => {
    render(<SessionHeader sessions={[S(2, { boss: true })]} session={S(2, { boss: true })} view="board" onView={() => {}} onAct={() => {}} onBudget={() => {}} />)
    expect(screen.getByTestId('ts-boss')).toBeInTheDocument()
  })
  it('renders nothing for a session that is not one', () => {
    const { container } = render(<BossBadge session={S(1)} />)
    expect(container).toBeEmptyDOMElement()
  })
})
